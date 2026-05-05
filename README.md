# Benngard_Iaac

Ansible-driven IaaC для Benngard-стека: четыре Exoscale VDS (dev, prod,
metrics, vpn), CI/CD оркестратор на metrics, observability на metrics
(VictoriaMetrics + Tempo + Loki + Grafana), отдельный vpn-хост для админ-VPN.

## Топология

| Host        | Назначение                                                            |
| ----------- | --------------------------------------------------------------------- |
| **dev**     | App-сервер для `dev.benngard.de` (backend + frontend SPA)             |
| **prod**    | App-сервер для `benngard.de` (backend + frontend SPA)                 |
| **metrics** | Observability-стек + CI/CD webhook orchestrator                       |
| **vpn**     | WireGuard endpoint для админ-доступа операторов к dev / metrics       |

Приложения деплоятся через GitHub Actions → push в Exoscale Container
Registry → webhook на metrics → SSH в dev/prod по приватной сети →
атомарная подмена контейнера / статики.

## Quick start

С нуля — [setup-from-scratch.md](docs/setup-from-scratch.md).

Операторские команды — `make help`.

## Два независимых track'a

| Контур | Команды |
|---|---|
| **Стандартные хосты** (dev/prod/metrics) | `make bootstrap` → `make provision` → `make deploy-app` |
| **vpn-хост** (single-purpose) | `make bootstrap-vpn` → `make provision-vpn` |

vpn намеренно вне `all_servers`: основной bootstrap/provision цикл его не
задевает, docker/nginx/certbot туда не ставятся.

## Документация

| Doc | Назначение |
|---|---|
| [setup-from-scratch.md](docs/setup-from-scratch.md) | Развёртывание с нуля + принципы работы с инфрой |
| [exoscale-infrastructure.md](docs/exoscale-infrastructure.md) | Канонические факты: IPs, SG, managed DB, DNS |
| [ci-cd-design.md](docs/ci-cd-design.md) | CI/CD архитектура, tag-стратегия, env management |
| [wireguard-client.md](docs/wireguard-client.md) | Настройка VPN-клиента |
| [auto-updates-and-reboot.md](docs/auto-updates-and-reboot.md) | Расписание автообновлений и canary reboot |
| [db-provider-migration.md](docs/db-provider-migration.md) | Исторический runbook: Replit → Exoscale Postgres |
