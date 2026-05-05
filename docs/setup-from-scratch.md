# Развёртывание инфры с нуля — список файлов

Что подготовить и где менять перед `make bootstrap` / `make provision` на чистых VDS.

> Канонические факты по текущей инфре (хостер, IP, пользователи, security
> groups, managed-DB) — в [exoscale-infrastructure.md](./exoscale-infrastructure.md).
> Этот документ описывает только пайплайн.

## Подготовить

- 3 SSH-ключа: `initial`, `madlord` (operator), `deploy-agent` (тот же используется и для metrics → app цепочки CI/CD)
- WireGuard-ключи пиров (`make wg-keygen`)
- Vault-секреты (Grafana, Telegram, пароль madlord, registry pull token, webhook secret)
- **`.vault_pass`** в repo root: пароль для ansible-vault (gitignored, mode 0600). Этим паролем зашифрованы и `vault.yml`, и `ansible/envs/.env.api.*`. Все make-таргеты подхватывают его автоматически через `ansible.cfg`. Если файла нет — vault-команды свалятся; для одноразового запроса с интерактивом — добавить `--ask-vault-pass` руками.
- DNS A-записи на dev / prod / metrics (на Exoscale выдаются при провижне VM, см. exoscale-infrastructure.md). vpn без DNS — клиенты ходят по IP в `Endpoint =` WG-конфига.
- Pub-часть `initial`-ключа в `/home/rockylinux/.ssh/authorized_keys` на каждом VDS (на Exoscale — выбрать SSH-key при создании инстанса; cloud-init разложит ключ в нужное место)
- **Exoscale Security Groups**:
  - dev / prod / metrics: `http-server` (80/443) + `madlord-ssh` (22 от 77.37.160.6/32)
  - vpn: `madlord-ssh` + `wireguard` (51820/udp от 0.0.0.0/0); HTTP не нужен
  - Если bootstrap'ишь не из своей статики — временно прицепи `public-ssh` (22 от 0.0.0.0/0), оторви после первого bootstrap'a
- **Exoscale Container Registry**: создать namespace `benngard`, сгенерить два token'а — `ci-push` (Push, Pull, Create Tag) и `server-pull` (Pull, List Tag). Первый идёт в GH Secrets, второй — в `vault.yml`.

## Файлы

| Файл                                     | Поля                                                                                                                                                  |
| ---------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------- |
| `ansible/group_vars/all/main.yml`        | `initial_ssh_private_key`, `madlord_ssh_private_key`, `deploy_agent_ssh_private_key`, `certbot_email`, `registry_url`                                  |
| `ansible/group_vars/dev_app.yml`         | `metrics_host`, `nginx_vhosts[].domain`, `static_root`                                                                                                |
| `ansible/group_vars/prod_app.yml`        | то же                                                                                                                                                 |
| `ansible/group_vars/metrics_server.yml`  | `nginx_vhosts[].domain`                                                                                                                               |
| `ansible/group_vars/vpn_server.yml`      | `wireguard_peers`, `wireguard_forward_targets`                                                                                                        |
| `.vault_pass`                            | пароль ansible-vault (одна строка, mode 0600, gitignored) — общий для vault.yml и всех envs                                                            |
| `ansible/group_vars/all/vault.yml`       | encrypted; plain — `vault.yml.plain` (gitignored). Bootstrap: `make vault-init` → `$EDITOR vault.yml.plain` → `make vault-encrypt`                     |
| `ansible/envs/.env.api.dev`              | encrypted; plain — `.env.api.dev.plain` (gitignored). `make vault-init` создаст `.plain` из `.env.api.example` → `$EDITOR` → `make vault-encrypt`     |
| `ansible/envs/.env.api.prod`             | то же для prod                                                                                                                                        |
| `ansible/inventory/hosts.ini`            | public IP + private_ip для всех VDS (создать копией из `hosts.ini.example`)                                                                           |
| `wireguard/keys/`                        | `<peer>.key` + `<peer>.pub`; `wg-server.pub` создастся ансиблом при provision-vpn                                                               |
| `~/.ssh/benngard-deploy-agent-exoscale`  | приватка deploy-agent: pubkey уже добавляется к `deploy-agent` на app-серверах (server_users), приватка копируется ансиблом на metrics в `/etc/benngard-deploy/id_ed25519` для CI-цепочки |

## Запуск

**Стандартный track** (dev / prod / metrics):
```bash
make bootstrap            # один раз за жизнь VDS (rockylinux@22)
make provision            # многократно при изменении конфига (madlord@22)
```

**VPN track** (отдельный single-purpose vpn-хост, не в `all_servers`):
```bash
make bootstrap-vpn        # один раз за жизнь VDS (rockylinux@22)
make provision-vpn        # многократно при добавлении пиров / правке WG ACL
```

Проверка:

```bash
ansible -i ansible/inventory/hosts.ini all -m ping
```

Дальше приложение деплоит CI из `Benngard_Backend` и `Benngard_Frontend`.

## CI/CD: что подцепить после первого provision'a

В каждом из репозиториев `Benngard_Backend` и `Benngard_Frontend` добавить
**три GH Secret'а**:

| Secret           | Значение                                                      |
| ---------------- | ------------------------------------------------------------- |
| `REGISTRY_USER`  | login Exoscale `ci-push` token'a                              |
| `REGISTRY_TOKEN` | password Exoscale `ci-push` token'a                           |
| `WEBHOOK_SECRET` | то же значение что `vault_webhook_secret` в `vault.yml.plain` |

После этого первый деплой:

```bash
# 1. в Backend repo
git tag v0.1.0-dev.1 && git push --tags

# 2. наблюдать через `gh run watch` или Telegram-канал метрик —
#    deploy ok / deploy FAILED идёт через Loki → Grafana alert.

# 3. в Frontend repo — аналогично
```

Архитектура и tag-стратегия — в [ci-cd-design.md](./ci-cd-design.md).

## Принципы работы с инфрой

- **IaaC = единственный источник правды**. Изменения в реальном состоянии серверов
  должны быть в репо. Временные ручные правки на сервере для диагностики — норма,
  но к концу сессии: либо фиксируются в IaaC, либо вручную откатываются. Drift
  ловится тем что следующий провижн / ребут / hardening reapply вернёт всё в
  состояние шаблона — лучше об этом узнать заранее.
- **Гэп: `hardening` роль вызывается только в `bootstrap.yml`** (one-shot). При
  изменении `roles/hardening/*` на работающем хосте обычный `make provision` НЕ
  подтянет изменения — bootstrap.yml повторно запускать нельзя (`rockylinux` user
  заблокирован после первой настройки). Варианты: синкать руками и обновлять IaaC
  одновременно, либо вынести `hardening` в `provision.yml`.
- **Role-локальный firewall** (`ansible.posix.firewalld`) предпочтительнее WG-условий
  в hardening-шаблоне. Сейчас `wireguard` сам открывает `UDP/51820` и `<forward/>`
  в public-зоне — `make provision-vpn` самодостаточен, не зависит от того
  чтобы кто-то прогнал hardening заново. Такой паттерн стоит держать и для других
  «опциональных» сервисов.

## Связанные доки

- [ci-cd-design.md](./ci-cd-design.md) — архитектура CI/CD, tag-стратегия, env management
- [auto-updates-and-reboot.md](./auto-updates-and-reboot.md) — стратегия автообновлений и ребута, что делать если dev упал во вторник
- [wireguard-client.md](./wireguard-client.md) — настройка VPN-клиента
- CVE-сканирование образов — `.github/workflows/trivy-scan.yml` (раз в неделю + на каждом PR с изменением compose)
- Bump'ы тегов upstream-образов — Dependabot, см. `.github/dependabot.yml`
