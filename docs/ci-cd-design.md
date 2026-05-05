# CI/CD design

Working document — фиксирует принятые решения и открытые вопросы перед
имплементацией. Удалить или пересмотреть после первого работающего деплоя.

## Цели

- Деплой Backend и Frontend в dev и prod
- GH Actions runner НЕ имеет SSH к app-серверам
- Секреты (env, registry tokens) **не в GH Secrets** — в `vault.yml` Iaac,
  обновляются через `make vault-decrypt` → `$EDITOR vault.yml.plain` → `make vault-encrypt` (см. [vault.yml.plain.example](../ansible/group_vars/all/vault.yml.plain.example))
- В GH Secrets — только то без чего нельзя (registry push token, webhook HMAC secret)
- Откат пока ручной (повторный webhook со старым тегом из registry — `5 last` retention)

## Высокоуровневый flow

```
[Backend repo / Frontend repo]
   tag push → GitHub Actions
   ↓
   1. tests
   2. docker build + push → exo.container-registry.com (ci-push token)
   3. trigger HTTP POST → https://metrics.benngard.de/deploy-gate/hooks/<id>
   ↓
[metrics.benngard.de]
   deploy-gate (Go service, native systemd, за host nginx /deploy-gate/)
   ↓ verify HMAC SHA256 (X-Hub-Signature-256)
   ↓ validate image regex
   ↓ post Telegram message with Approve / Deny buttons (out-of-band 2FA)
   ↓ ack GH 202 (deploy is async, runner doesn't wait)
   ╎
   ╎  ── approver taps Approve in Telegram ──
   ╎
   ↓ verify callback_query.from.id ∈ approver whitelist
   ↓ ssh dev|prod (через приватку 10.0.0.0/24, deploy-agent user)
   ↓
[dev / prod app server]
   docker pull (server-pull token)
   backend: docker compose up -d → healthcheck /health
   frontend: docker create + docker cp /dist → atomic rsync в /var/www/benngard-frontend
```

Защита от компрометации GH аккаунта: даже с утечкой `WEBHOOK_SECRET` +
registry push token атакер не может задеплоить — апрув приходит из
Telegram'а, `callback_query.from.id` ставится серверами TG и не подделать
через bot token. См. [deploy-gate/README.md](../deploy-gate/README.md).

## Разделение ответственности

Цепочка из двух компонентов с **разными зонами ответственности**:

- **deploy-gate** на metrics-хосте решает **ЧТО деплоить**: принимает
  webhook от GH, валидирует HMAC и image-tag regex, получает human-апрув
  через Telegram, потом по SSH вызывает удалённый скрипт. Ничего не знает
  про docker-compose, healthcheck endpoint'ы, atomic rsync.

- **`deploy.sh`** на app-сервере (rendered ансиблом из
  `roles/{backend,frontend}/templates/deploy.sh.j2`) решает **КАК деплоить**:
  `docker pull`, `docker compose up -d` + curl-loop healthcheck, либо
  `docker create + cp` + atomic rsync. Ничего не знает про webhook'и, HMAC,
  TG-апрувы — просто получает на вход `BACKEND_IMAGE=…` или
  `FRONTEND_IMAGE=…` и делает свою работу.

**Зачем такая граница:**
- Каждый компонент маленький, по одному назначению — легче читать и
  тестировать
- Можно вручную перезапустить deploy.sh по SSH когда дебажишь — без
  поднятия всего CI-pipeline'а
- Если меняется container runtime (например, docker → podman) — правится
  только deploy.sh, deploy-gate не трогаем
- Если меняется auth flow (например, добавляем второй approver) —
  правится только deploy-gate, deploy.sh не в курсе

## Tag strategy

| Pattern | Что триггерит | Где должен лежать |
|---|---|---|
| `vX.Y.Z-dev.N` | deploy в **dev only** | любая ветка |
| `vX.Y.Z` | deploy в **dev И prod одновременно** | только `main` |

Источники версий:
- Backend: `pyproject.toml` (version field)
- Frontend: `package.json` через `npm version`

GH workflow (псевдокод per repo):
```yaml
on:
  push:
    tags: ['v*.*.*', 'v*.*.*-dev.*']
jobs:
  build-push: ...
  deploy-dev:
    if: startsWith(github.ref, 'refs/tags/v')
  deploy-prod:
    if: startsWith(github.ref, 'refs/tags/v') && !contains(github.ref, '-dev.')
```

## Registry

`exo.container-registry.com` — Exoscale. Два токена:

| Token | Где живёт | Права |
|---|---|---|
| `ci-push` | GH Secret в каждом из Backend/Frontend repo | Push, Pull, Create Tag |
| `server-pull` | `vault.yml` в Iaac | Pull, List Tag |

`server-pull` Ansible пишет на app-серверы (`/etc/docker/config.json` или через `community.docker.docker_login` модуль).

Retention в registry — 5 последних `v*.*.*` и 5 последних `v*.*.*-dev.*`.

## Backend deploy

### Docker image

В Backend repo `infra/Dockerfile` — uv multi-stage уже есть, рабочий. Build context = repo root:
```bash
docker build -f infra/Dockerfile -t exo.container-registry.com/<org>/benngard-backend:$TAG .
```

### Compose на app-сервере

Текущий [Backend/infra/docker-compose.yml](../../Benngard_Backend/infra/docker-compose.yml) — переедет
в Iaac (`infra/{dev,prod}/docker-compose.app.yml`). `${APP_IMAGE}` env var
определяется webhook orchestrator'ом при триггере.

### Migrations

`AUTO_MIGRATE=true` в lifespan. Tradeoffs осознаны (см. комменты в
[Backend/.env.example](../../Benngard_Backend/.env.example)). На каждом деплое
lifespan делает `alembic upgrade head` до запуска uvicorn — атомарный переход
со старого container на новый, между ними DB ничего не пишет (старый
container уже остановлен).

**Перед первым prod-деплоем**: одноразово выполнить на Neon prod_db и dev_db:
```sql
UPDATE alembic_version SET version_num = '6a0dbe92fe29';
```
(legacy state с Replit'а на ID `rss_feed_001` — ground-truth схема DB
совпадает с reverse-engineered initial_schema, см. история обсуждения).

### Healthcheck в deploy.sh

90 секунд timeout, 3-секундный interval, 30 проб максимум.

```bash
docker pull "$BACKEND_IMAGE"
APP_IMAGE="$BACKEND_IMAGE" docker compose up -d --remove-orphans
for i in {1..30}; do
  curl -sf -m 2 http://127.0.0.1:8000/health && exit 0
  sleep 3
done
docker compose logs --tail 200 app
exit 1
```

### Env

**Backend env** хранится **per-file ansible-vault encrypted** в `ansible/envs/`:
- `ansible/envs/.env.api.dev` — encrypted, content для dev
- `ansible/envs/.env.api.prod` — encrypted, content для prod

Внутри после `ansible-vault edit` — обычный .env синтаксис без YAML-обёртки:
```
DATABASE_URL=postgres://...neon.../dev_db
SESSION_SECRET=...
OPENROUTER_API_KEY=...
ADMIN_EMAILS=anton@benngard.de,...
ENVIRONMENT=production
AUTO_MIGRATE=true
LOG_LEVEL=INFO
OTEL_ENABLED=true
OTEL_ENDPOINT=http://otel-collector:4318
OTEL_SERVICE_NAME=benngard-backend
```

**Почему так**: один файл = один env, нативный .env-синтаксис без квочек/escape.
Workflow единый с `vault.yml`: `make vault-decrypt` → `$EDITOR <file>.plain`
→ `make vault-encrypt`. Plain версии gitignored, encrypted коммитятся.

Ansible-роль `backend` использует `ansible.builtin.copy` который
auto-decrypt'ит vault-encrypted source-файлы (vault password из `.vault_pass`
через `ansible.cfg`):
```yaml
- name: Push api .env
  ansible.builtin.copy:
    src: "envs/.env.api.{{ deploy_env }}"
    dest: /opt/benngard-app/.env
    owner: madlord
    group: deploy-agent
    mode: '0640'
  notify: Restart benngard-backend
```

**Frontend env** — Vite/React build-time vars (`VITE_SENTRY_DSN`,
`VITE_MIXPANEL_TOKEN`, etc.) публичные, baked в JS bundle при сборке.
Лежат **в Frontend repo** как `.env.dev` / `.env.prod` plaintext, закоммичены вместе с кодом.

**Почему НЕ в Iaac** (изначально хотели единое место для всех env, но
переиграли): vite-вары часть build process'а, build идёт в GH Action на
checkout'е Frontend repo. Положить в Iaac → потребуется cross-repo
checkout с PAT'ом, build-аргумент копирования и т.д. — лишний кор. У
Frontend env нет настоящих секретов чтобы оправдывать сложность.

**Frontend build асимметричен от backend**: vite вкомпиливает env в JS на
этапе build (не deploy), значит на каждый release нужны ДВА image:
- `:vX.Y.Z-dev.N` (dev env baked) — для dev деплоя
- `:vX.Y.Z` (prod env baked) — для prod деплоя
- `:vX.Y.Z-dev` (dev env baked, derived from clean prod tag) — для dev деплоя при clean prod tag

Backend от этого не страдает — у него один image работает на любой env'е, отличие только в `.env` на сервере.

Update env workflow:
```bash
make vault-decrypt                                   # <file> → <file>.plain (для всех VAULT_FILES)
$EDITOR ansible/envs/.env.api.prod.plain             # править как обычный .env
make vault-encrypt                                   # <file>.plain → <file>
make deploy-app-env ANSIBLE_OPTS='--limit prod_app'  # Ansible push на сервер
# Restart benngard-backend handler рестартанёт контейнер если .env реально изменился
```

Vault-команды унифицированы (см. Makefile): `vault-init` создаёт недостающие
`<file>.plain` из шаблонов, `vault-encrypt`/`vault-decrypt` итерируются по
`VAULT_FILES` списку. `*.plain` в .gitignore.

## Frontend deploy

### Dockerfile (новый, в Frontend repo)

```dockerfile
FROM node:20-alpine AS builder
WORKDIR /app
COPY package*.json ./
RUN npm ci
COPY . .
RUN npm run build

FROM scratch
COPY --from=builder /app/dist /
```

`FROM scratch` финальный stage — image как контейнер для артефакта.
Не запускается, используется только как **транспорт** через registry.

### deploy.sh на app-сервере (frontend часть)

```bash
docker pull "$FRONTEND_IMAGE"
cid=$(docker create "$FRONTEND_IMAGE")
staging="/var/www/${DOMAIN}.tmp"
final="/var/www/${DOMAIN}"
rm -rf "$staging" && mkdir -p "$staging"
docker cp "$cid:/." "$staging/"
docker rm "$cid"
rsync -a --delete "$staging/" "$final/"   # atomic swap
rm -rf "$staging"
```

Atomicity через rsync с staging dir — host nginx ни секунды не видит partial state.

## deploy-gate orchestrator (metrics)

In-tree Go service (`Benngard_Iaac/deploy-gate/`) под нативным systemd.
Заменил `adnanh/webhook` + shell-обвязку в мае 2026 (см. историю в
[deploy-gate/README.md](../deploy-gate/README.md) — там же threat model).

**Почему свой**: добавили **out-of-band Telegram 2FA** на каждый деплой.
adnanh/webhook не поддерживает approval-flow, и шить его сбоку через
shim усложняло state-handling pending-approvals. Один Go-процесс на 4
эндпоинта + TG-callback проще схемы из двух сервисов.

**Не контейнеризован** по той же причине что был adnanh: один Go-binary
со stdlib-only, journald → promtail → Loki автоматически, format
`event=<start|ok|FAILED|...>` сохранён → существующий Loki alert rule
работает без изменений.

Установка (роль `deploy_gate`, см. `ansible/roles/deploy_gate/tasks/main.yml`):
- **Сборка локально**, не на metrics: `make build-deploy-gate`
  cross-компилит linux/amd64 в `deploy-gate/dist/deploy-gate`,
  предварительно прогнав `go test`. Бинарь gitignored.
- `make provision` зависит от `build-deploy-gate` — собирается
  автоматически перед прогоном Ansible'я.
- Ансибл-таск `Install deploy-gate binary` копирует `dist/deploy-gate`
  → `/usr/local/bin/deploy-gate` с детектом byte-identity (нет
  изменений → no-op, restart не дёргаем).
- systemd unit `deploy-gate.service` (User=deploy-gate, EnvironmentFile=
  `/etc/benngard-deploy/deploy-gate.env`, `-config .../config.json`,
  `-ssh-config .../ssh-config`)
- hardening: NoNewPrivileges, ProtectSystem=strict, ProtectHome,
  PrivateTmp, ReadOnlyPaths
- listener 127.0.0.1:9000 — публично только через nginx
- deploy-gate сам вызывает Telegram `setWebhook` при старте с
  эпhemeral path-secret (генерится в памяти, в vault не хранится).

Promotion path для будущего: когда команда > 1 контрибьютора, build
переезжает в GH Actions (с tag'ами вместо локальной сборки) — устраняет
зависимость от Go-версии разработчика.

### Эндпоинты

| Path | Origin | Auth |
|---|---|---|
| `POST /hooks/deploy-backend-dev` | GH Actions | HMAC SHA256 (`X-Hub-Signature-256`) |
| `POST /hooks/deploy-backend-prod` | GH Actions | HMAC SHA256 |
| `POST /hooks/deploy-frontend-dev` | GH Actions | HMAC SHA256 |
| `POST /hooks/deploy-frontend-prod` | GH Actions | HMAC SHA256 |
| `POST /tg/callback/<path-secret>` | Telegram | path-secret + TG IP-allowlist (nginx) + `callback_query.from.id ∈ approver_ids` |
| `GET /health` | local | none, для wait_for |

Каждый `/hooks/*` принимает JSON `{"image": "..."}`, проверяет HMAC +
image regex, кладёт pending approval в in-memory store (UUID), постит
сообщение с inline-кнопками в TG-тред deploys, возвращает 202.

`/tg/callback/<path-secret>` — Telegram bot webhook URL. Принимает
Update'ы с `callback_query`, парсит `approve:<uuid>` или `deny:<uuid>`,
по approve выстреливает SSH-деплой в goroutine + edit'ит сообщение.

### nginx changes (на metrics.benngard.de)

```nginx
location /hooks/ {
    proxy_pass http://127.0.0.1:9000/hooks/;
    proxy_read_timeout 60s;          # deploy-gate возвращает 202 быстро
    # без whitelist по IP — auth через HMAC
}

location ~ ^/tg/callback/ {
    allow 149.154.160.0/20;          # Telegram official ranges
    allow 91.108.4.0/22;
    deny  all;
    access_log /var/log/nginx/tg_callback.log tg_callback_redacted;
    proxy_pass http://127.0.0.1:9000;
    proxy_read_timeout 10s;
}
```

`tg_callback_redacted` — кастомный `log_format` в том же vhost-файле,
заменяющий request-line на `POST /tg/callback/REDACTED` чтобы
path-secret не падал в access.log/Loki в открытом виде.

### Approval flow в деталях

1. GH POST `/hooks/deploy-backend-prod` с `{image: "...:v1.2.3"}` + HMAC
2. deploy-gate verify HMAC + image regex → создаёт `Pending{uuid, ...}`
3. Bot шлёт сообщение в тред "deploys" с кнопками **✅ Approve** / **❌ Deny**:
   ```
   🚀 Deploy approval needed
   service: backend
   env:     prod
   image:   exo.container-registry.com/.../benngard-backend:v1.2.3
   created: 2026-05-20T14:32:01Z
   ```
4. Возвращает GH Action 202 (runner завершается зелёным — фактический
   результат деплоя приходит в TG, не через HTTP-статус GH)
5. Approver тапает кнопку. TG отправляет `callback_query` к
   `/tg/callback/<path-secret>`
6. deploy-gate проверяет `from.id ∈ approver_ids` (whitelist в vault'е),
   парсит `approve:<uuid>` или `deny:<uuid>`, atomically Resolve'ит entry
   (race-safe с reaper'ом)
7. На approve → edit сообщения "⏳ Approved by @anton, deploying..." →
   spawn goroutine с `ssh dev "BACKEND_IMAGE=... /opt/benngard-app/deploy.sh"`
8. По завершении SSH → edit сообщения "✅ deploy ok" или "🔥 FAILED" +
   tail последних 20 строк stderr

10-минутный auto-deny через reaper goroutine (TTL = config.approval_timeout_seconds).
Concurrent деплои на разные таргеты — каждое pending независимое UUID.

### SSH chain metrics → app (без изменений)

Используется существующий `deploy-agent` keypair (pubkey authorized на
app-серверах через server_users):
- privkey оператора: `~/.ssh/benngard-deploy-agent-exoscale`
- Ansible копирует приватку на metrics в `/etc/benngard-deploy/id_ed25519`
  (owner=deploy-gate, mode 0600), вместе с `known_hosts` (через ssh-keyscan)
  и `ssh-config` с aliases `dev` и `prod` → private IP.
- Go-сервис шеллит `ssh -F /etc/benngard-deploy/ssh-config <alias> "<cmd>"`

`deploy-agent` member of `docker` group (без wheel) — может `docker pull/
compose up`, не может `sudo`. Минимально достаточные права.

### Vault keys

```yaml
vault_webhook_secret:                       # HMAC SHA256, GH ↔ deploy-gate
vault_telegram_bot_token:                   # shared с Grafana alerts
vault_telegram_chat_id:                     # same chat as alerts
vault_telegram_thread_deploys:              # separate thread под апрувы
vault_telegram_approvers:                   # [user_id, ...] whitelist
# (path-secret для /tg/callback/<...> — ephemeral, генерится в памяти при
# старте сервиса, в vault'е НЕ хранится)
```

## Alerting на failure

- deploy-gate stdout/stderr → journald → promtail → Loki
  (SyslogIdentifier=`deploy-gate`, заменил `webhook` при миграции)
- Существующая Grafana alert rule парсит `event=ok|FAILED` lines, формат
  log-сообщений сохранён → правило работает без изменений
- Шлёт в Telegram-prod тред

В дополнение к alert rule, TG-сообщение с финальным статусом edit'ится
самой deploy-gate — failure сразу видно в треде "deploys" с tail'ом
stderr.

## Имплементация

### Iaac (✅ done)

- [x] Роль `backend` — copy compose, push vault-encrypted .env, render deploy.sh, docker login
- [x] Роль `frontend` — render deploy.sh (docker pull → docker cp → atomic rsync)
- [x] Роль `deploy_gate` (бывш. `webhook`) — in-tree Go service `Benngard_Iaac/deploy-gate/`, локальная cross-compile через `make build-deploy-gate`, ansible копирует бинарь на metrics, systemd unit, /etc/benngard-deploy/{config.json,deploy-gate.env,ssh-config,id_ed25519,known_hosts}
- [x] Расширение `nginx` role: `hooks_proxy: true` рендерит location `/hooks/`
- [x] SSH-chain metrics → app использует существующий `deploy-agent` ключ (без отдельного `metrics-deploy-agent`)
- [x] vault: `vault_registry_pull_username`, `vault_registry_pull_token`, `vault_webhook_secret`, `vault_telegram_thread_deploys`, `vault_telegram_approvers` (TG path-secret больше не в vault'е — ephemeral)
- [x] vault.yml.plain.example: соответствующие placeholder'ы
- [x] group_vars/all/main.yml: `registry_url`, `metrics_deploy_agent_ssh_pubkey`, deploy paths
- [x] inventory: `private_ip` per host (для ssh-конфига внутри webhook)
- [x] `ansible/envs/.env.api.example` — шаблон
- [ ] `ansible/envs/.env.api.{dev,prod}` — заполнить и `make encrypt-env-new` (operator step)
- [x] `infra/{dev,prod}/docker-compose.app.yml`
- [x] `infra/metrics/docker-compose.yml` (стек метрик без webhook'а — webhook теперь нативный systemd сервис)
- [x] Makefile: `vault-init`, `vault-encrypt`, `vault-decrypt`, `deploy-app`, `deploy-app-env`
- [x] new playbook `ansible/app.yml`
- [x] docs/setup-from-scratch.md update

### Backend repo (✅ done)

- [x] `.github/workflows/release.yml` — test → build-push → deploy-dev → deploy-prod (только clean tag)
- [ ] GH Secrets `REGISTRY_USER`, `REGISTRY_TOKEN`, `WEBHOOK_SECRET` (operator step)

### Frontend repo (✅ done)

- [x] `Dockerfile` (multi-stage → scratch, ARG ENV_FILE)
- [x] `.env.dev` и `.env.prod` plaintext
- [x] `.dockerignore`
- [x] `.github/workflows/release.yml` — build-dev (всегда) + build-prod (только clean) → deploy
- [ ] GH Secrets `REGISTRY_USER`, `REGISTRY_TOKEN`, `WEBHOOK_SECRET` (operator step)

## Решённые вопросы (зафиксировано)

1. Image tag strategy — `vX.Y.Z` (prod+dev) и `vX.Y.Z-dev.N` (dev only)
2. Migrations — AUTO_MIGRATE=true в lifespan, tradeoffs осознаны
3. Registry — Exoscale, два token'а (ci-push, server-pull)
4. Healthcheck — 90s, 3s interval
5. Rollback в MVP не делаем (повторный webhook со старым тегом)
6. Один домен на env (frontend / + backend /api/), CORS не нужен
7. SSH alias через `~/.ssh/config` (Ansible-rendered) на metrics
8. Workflow в каждом репо отдельно, не reusable
9. Деплоит — `deploy-agent` user (уже есть, в group docker)
10. Frontend — образ `FROM scratch + dist`, на app-сервере через `docker cp` + atomic rsync
11. Env management — **backend** в Iaac (`ansible/envs/.env.api.{dev,prod}` ansible-vault encrypted, edit через `make edit-env target=api.prod`); **frontend** в Frontend repo (`.env.dev` / `.env.prod` plaintext закоммичено, vite-вары публичные)
12. Webhook auth — HMAC SHA256 (defence layer 1) + Telegram approval (defence layer 2, out-of-band). См. [deploy-gate/README.md](../deploy-gate/README.md) для threat-model.
13. Алертинг на failure — Grafana rule `cicd-deploy-failed` на любой `deploy event=FAILED` в Loki, env extracted через `pattern` → routing в правильный TG-тред через notification policy. Дополнительно `cicd-deploy-invalid` на битый payload (HMAC валиден, image format нет — потенциальный signal misconfig/attack)
14. Дашборд `cicd-deploys.json` — Stat (24h success/fail/start/invalid) + logs panel с фильтрами Service / Env

## Открытые вопросы

1. **Healthcheck timeout 90s достаточно?** — пересмотреть после первого
   реального прод-деплоя с миграцией.

## deploy-gate migration runbook

Когда переключаемся с adnanh/webhook на deploy-gate (одноразово; роль
`deploy_gate` идемпотентно зачищает старое):

### Pre-flight (на dev-машине)

1. **Создать тред "deploys"** в TG alerts-чате, скопировать message_thread_id.
   Заменить placeholder `vault_telegram_thread_deploys` в
   `vault.yml.plain` на реальный id (или подменить на уже сохранённый).
2. **Сгенерировать новые секреты** уже сделано через `awk` патч (см.
   историю коммитов): `vault_webhook_secret` (rotate) и
   (path-secret теперь ephemeral — генерится при старте, не нуждается в ротации).
3. **`make vault-encrypt`** — закоммитить обновлённый `vault.yml`.
4. **Обновить GH Secret `WEBHOOK_SECRET`** в обоих репо
   (Benngard_Backend и Benngard_Frontend) на новое значение из vault.

### Apply

5. `make provision ANSIBLE_OPTS='--limit metrics_server'`
   - Останавливает + удаляет старый `webhook.service` + бинарь + `/opt/webhook`
   - dnf install golang
   - Билдит свежий deploy-gate из `Benngard_Iaac/deploy-gate/`
   - Запускает systemd unit `deploy-gate.service`
   - Через `ansible.builtin.uri` регистрирует TG webhook URL
   - Renders новый nginx vhost с `/tg/callback/` location
   - `Reload nginx` через handler

### Smoke test

6. **Approve path**:
   ```bash
   git tag v0.0.0-deploygate-test1.dev.1
   git push origin v0.0.0-deploygate-test1.dev.1
   ```
   GH workflow билдит dev-image, бьёт `/hooks/deploy-backend-dev`.
   Ожидание: в TG-треде "deploys" появляется approval сообщение в течение
   30 сек. Approver тапает ✅. Сообщение редактируется → "deploying" →
   "deploy ok". На dev отрабатывает `/opt/benngard-app/deploy.sh`.

7. **Deny path**:
   ```bash
   git tag v0.0.0-deploygate-test2.dev.1
   git push origin v0.0.0-deploygate-test2.dev.1
   ```
   Approver тапает ❌. Сообщение редактируется → "Denied". На dev
   ничего не запускается. `docker ps` показывает старый контейнер.

8. **Timeout path**:
   ```bash
   git tag v0.0.0-deploygate-test3.dev.1
   git push origin v0.0.0-deploygate-test3.dev.1
   ```
   Не трогать кнопки 10 минут. Ожидание: сообщение редактируется в
   "⏱ Timeout, auto-denied".

9. **Unauthorized rejection**: попросить не-whitelisted TG-юзера тапнуть
   approve. Ожидание: callback ignored, в `journalctl -u deploy-gate` —
   `tg: unauthorized callback from id=...`, в TG юзер видит "Not authorized".

### Real release

10. После того как все три smoke-теста зелёные, обычный prod-релиз:
    `git tag v1.x.y` на main, approve вручную в TG.

### Откат (если что-то пошло не так)

Старая adnanh/webhook полностью удалена ролью. Откат = revert коммита
с миграцией + `make provision` (восстановит старый webhook). Между
прогонами провижна старый GH Secret `WEBHOOK_SECRET` уже ротирован — на
откате надо ротировать обратно в GH или оставить новое значение в vault.
