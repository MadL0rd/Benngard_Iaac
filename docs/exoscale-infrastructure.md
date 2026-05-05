# Exoscale Infrastructure

**Provider:** Exoscale — CH-DK-2 (Zurich)  
**OS:** Rocky Linux 10 64-bit  
**User:** `rockylinux`

## Servers

| Name    | Type            | CPU | RAM   | Disk  |
| ------- | --------------- | --- | ----- | ----- |
| prod    | Standard Medium | 2   | 4GB   | 10GB  |
| dev     | Standard Small  | 2   | 2GB   | 10GB  |
| metrics | Standard Small  | 2   | 2GB   | 100GB |
| vpn     | Standard Micro  | 1   | 512MB | 10GB  |

## Network

**Private Network:** `Benngard`

- Zone: CH-DK-2
- ID: `be6d4a1d-7cd0-4ffa-ae10-a626794a8c66`
- Type: MANAGED (DHCP)
- Subnet: `10.0.0.0/24` (netmask `255.255.255.0`)
- DHCP pool: `10.0.0.1` – `10.0.0.150`

**Private IPs (DHCP-leased):**

| Server  | Private IP                              |
| ------- | --------------------------------------- |
| dev     | 10.0.0.40                               |
| prod    | 10.0.0.28                               |
| metrics | 10.0.0.138                              |
| vpn     | (не в приватке — single-purpose host)   |

OTel-трафик (app → metrics, порты 8428/4318/3100) ходит по приватной сети —
Exoscale Security Groups к приватному интерфейсу не применяются, поэтому
дополнительных SG-правил для observability не нужно.

## Security Groups

| Группа      | Протокол | Порт    | Источник       |
| ----------- | -------- | ------- | -------------- |
| http-server | TCP      | 80, 443 | 0.0.0.0/0      |
| madlord-ssh | TCP      | 22      | 77.37.160.6/32 |
| public-ssh  | TCP      | 22      | 0.0.0.0/0      |
| wireguard   | UDP      | 51820   | 0.0.0.0/0      |

> `public-ssh` и `madlord-ssh` деактивированы в обычном состоянии. Подключаются только на время настройки/обслуживания.

**Назначение:**

- prod: `http-server`
- dev: `http-server`
- metrics: `http-server`
- vpn: `madlord-ssh` + `wireguard` (HTTP не нужен)

## Database

**Managed PostgreSQL** — hobbyist-2 (1 node, 2 CPU, 2GB RAM, 8GB storage)  
Зона: CH-DK-2, доступ только через приватную сеть.

Базы: `prod_db`, `dev_db`

## DNS

| Запись              | IP             |
| ------------------- | -------------- |
| dev.benngard.de     | 91.92.141.48   |
| benngard.de         | 91.92.140.173  |
| metrics.benngard.de | 91.92.202.162  |

vpn без DNS-записи — клиенты идут на него по IP в `Endpoint =`
WG-конфигов (auto-собирается в Makefile из inventory).
