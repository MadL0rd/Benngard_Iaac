# Auto-updates & reboot

Стратегия: security-патчи приезжают сами через `dnf-automatic`, ребут только когда реально нужен (`needs-restarting -r`), dev — канарейка раньше остальных.

## Расписание (Berlin TZ)

| Когда    | Что                                            | Где                  |
| -------- | ---------------------------------------------- | -------------------- |
| Вт 03:00 | `dnf-automatic` устанавливает security-патчи   | dev + prod + metrics |
| Вт 04:00 | reboot если `needs-restarting -r` говорит «да» | dev (canary)         |
| Вт днём  | Оператор смотрит дашборд / алерты — жив ли dev | человек              |
| Ср 04:00 | reboot если `needs-restarting -r` говорит «да» | prod + metrics       |

24-часовая разница между dev и остальными — окно для оператора, чтобы успеть заметить регрессию и вмешаться.

## Если dev умер во вторник

```bash
# 1. Срочно — отключить авто-ребут на prod + metrics, чтобы они не повторили судьбу
make disable-canary-reboot

# 2. Зайти на dev (если SSH работает) или через KVM-консоль хостера, посмотреть что обновилось:
ssh dev.benngard.de "sudo dnf history list" | head -5

# 3. Откатить плохую транзакцию (id из шага 2):
ssh dev.benngard.de "sudo dnf history rollback <id>"
ssh dev.benngard.de "sudo systemctl reboot"

# 4. Когда dev жив и работает — включить обратно:
make enable-canary-reboot
```

⚠️ `make disable-canary-reboot` действует до следующего `make provision` — provision принудительно включает таймер обратно. Если хочешь длительное отключение — выставь `auto_reboot_enabled: false` в group_vars соответствующей группы.

## Проверка состояния

```bash
# Когда следующий запуск таймеров?
ssh dev.benngard.de "systemctl list-timers dnf-automatic.timer auto-reboot.timer"

# Что делал auto-reboot за последние 7 дней?
ssh dev.benngard.de "journalctl -t auto-reboot --since '7 days ago' --no-pager"

# Нужен ли ребут прямо сейчас?
ssh dev.benngard.de "needs-restarting -r"
# exit 0 = не нужен, exit 1 = нужен (ядро/glibc/openssl/systemd обновлялись)

# Что конкретно требует ребута?
ssh dev.benngard.de "needs-restarting -s"  # сервисы с устаревшими бинарниками
ssh dev.benngard.de "needs-restarting -k"  # ядро устарело vs running
```

## Ручной ребут prod / metrics

Если оператор решил, что dev выжил и пора ребутить остальные не дожидаясь Wed 04:00:

```bash
ssh benngard.de "needs-restarting -r || sudo systemctl reboot"
ssh metrics.benngard.de "needs-restarting -r || sudo systemctl reboot"
```

`|| systemctl reboot` сработает только если `needs-restarting -r` вернул код 1 (нужен ребут). На «всё ок» команда тихо завершится без ребута.

## Изменение расписания

В `ansible/group_vars/`:

- `all/main.yml` — дефолт для всех (`auto_reboot_oncalendar: "Wed 04:00"`)
- `dev_app.yml` — override для dev (`auto_reboot_oncalendar: "Tue 04:00"`)

После правки — `make provision`. Формат значения: systemd OnCalendar (см. `man systemd.time`).

Расписание самого `dnf-automatic.timer` (когда применяются патчи) — в `roles/common/tasks/main.yml`, OnCalendar=Tue 03:00. Менять там же.

## Полное отключение auto-reboot

На отдельной группе хостов — добавь в её group_vars:

```yaml
auto_reboot_enabled: false
```

И запусти `make provision` (после ручного `systemctl disable --now auto-reboot.timer` если нужно сразу).

Глобально — то же самое в `group_vars/all/main.yml`.

`dnf-automatic` при этом продолжит ставить патчи — просто без авто-ребута. Ребут останется ручной задачей (см. секцию выше).

## Известные ограничения

- **Алертов сейчас нет.** Узнаёшь о проблеме глядя на дашборд или по внешнему uptime monitor (если настроен). TODO: Loki ловит `journalctl -t auto-reboot`, Grafana алертит в TG.
- **60-секундное окно** между логом «rebooting in 60s» и собственно ребутом — единственный шанс отменить (`systemctl stop auto-reboot.service`).
- **Нет post-reboot health check** — если хост загрузился, но docker compose не поднялся, это не отлавливается. На радаре.
- **`upgrade_type = security` не покрывает `bugfix`-патчи и kernel CVE без security-классификации.** Раз в квартал стоит вручную: `ansible -i ansible/inventory/hosts.ini all -b -m dnf -a 'name=* state=latest' --ask-vault-pass`.
