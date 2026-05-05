# WireGuard — настройка клиента

VPN-сервер на отдельном **vpn**-хосте (Exoscale Standard Micro, single-purpose).
Даёт операторам без статического IP админ-доступ к:

- `dev.benngard.de` — front + `/api/*` (HTTPS/443)
- `metrics.benngard.de` — Grafana (HTTPS/443)

Всё остальное (SSH, prod, любые другие порты) режется серверным iptables.
ACL декларативный: `wireguard_forward_targets` в `group_vars/vpn_server.yml`,
шаблон `wg0.conf.j2` рендерит правила.

Split tunnel на клиенте — дефолтный роут не перехватывается. Через туннель
идёт только трафик к публичным IP `dev` и `metrics` (плюс VPN-подсеть
`10.10.0.0/24`). Никаких `/etc/hosts`-костылей: клиент резолвит домены
обычным публичным DNS, трафик к dev/metrics public IP уходит в туннель,
vpn-хост NAT'ит на свой public IP, на dev/metrics приходит запрос с
whitelisted IP → nginx пропускает.

---

## Пользователи и VPN-адреса

| Пользователь | VPN IP    |
| ------------ | --------- |
| anton_tek    | 10.10.0.2 |
| mkibardin    | 10.10.0.3 |
| andreas_belo | 10.10.0.4 |

---

## Для оператора (madlord) — подготовка конфигов

```bash
make wg-keygen           # сгенерить ключи пиров (skip существующие)
make provision-vpn       # развернуть на vpn-хосте, забрать серверный pubkey
make wg-configs          # собрать .conf для каждого пира
```

Готовые конфиги — в `wireguard/keys/<user>.conf`. Передавать пирам
защищённым каналом (файл содержит приватный ключ).

---

## Для пользователя — подключение

### macOS / Linux

```bash
# macOS
brew install wireguard-tools

# Linux (Ubuntu/Debian)
sudo apt install wireguard
```

```bash
sudo wg-quick up /path/to/user.conf
sudo wg-quick down /path/to/user.conf
```

### iOS / Android

Импортировать `.conf` через приложение WireGuard (файл или QR-код).

QR-код:
```bash
qrencode -t ansiutf8 < wireguard/keys/user.conf
```

После подключения — открыть `https://metrics.benngard.de` или
`https://dev.benngard.de` в браузере. Должно работать без дополнительных
настроек на стороне клиента.

---

## Архитектурно — почему так

WG-endpoint живёт на отдельном vpn-хосте, не на metrics. Это позволяет
клиентам без routing-loop'а класть и `metrics-public-IP` и `dev-public-IP`
в `AllowedIPs` (loop возникал бы если endpoint и destination — один и тот
же IP). Клиент шлёт запрос на публичный IP metrics → роутит через туннель →
vpn декриптит и MASQUERADE'ит на свой публичный IP → metrics видит запрос
с `vpn-public-IP`, который явно прописан в `nginx_whitelist_ips`
(см. `group_vars/all/main.yml`).

Тот же путь для dev.

---

## Добавить нового пользователя

1. Добавить `user:10.10.0.X` в Makefile → `WG_PEERS` и `wg-configs` for-loop
2. Добавить пира в `ansible/group_vars/vpn_server.yml` → `wireguard_peers`
3. `make wg-keygen && make provision-vpn && make wg-configs`
4. Передать пользователю его `.conf`
