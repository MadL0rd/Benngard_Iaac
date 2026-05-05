# deploy-gate

CI/CD orchestrator with **out-of-band Telegram approval** as a second factor
on top of the GitHub-side HMAC. Drop-in replacement for `adnanh/webhook` on
the metrics host.

## Threat model

GitHub account compromise leaks both the registry push token and the
HMAC webhook secret. With only HMAC in front of the deploy script an
attacker can push a malicious image and trigger a deploy → RCE on the
target host.

deploy-gate adds an approval step on a channel the attacker does not
control: every incoming webhook posts an inline-keyboard message to a
private Telegram thread; deploy proceeds only after a whitelisted
`telegram_user_id` presses **Approve**. Telegram's `callback_query.from.id`
is set server-side by Telegram and cannot be spoofed via the bot token.

## Flow

```
GH Action ──HMAC POST──▶ /hooks/deploy-<service>-<env>
                          │
                          ▶ verify HMAC, parse {image}
                          ▶ create Pending{uuid, image, service, env}
                          ▶ post TG message with Approve/Deny buttons
                          ▶ 202 Accepted to GH (do not block runner)

Approver tap ──▶ Telegram ──webhook──▶ /tg/callback/<path-secret>
                          │
                          ▶ verify from.id ∈ approver_ids
                          ▶ resolve pending by uuid
                          ▶ edit TG message: "Approved, deploying..."
                          ▶ ssh <env-alias> "<service-deploy-cmd>"
                          ▶ edit TG message: ok / fail + stderr tail
```

Pending TTL = 10 min, auto-deny on expiry.

## Logging

All events go to stdout (journald via systemd → promtail → Loki). The
existing Grafana alert rule parses `event=start|ok|FAILED|invalid` lines
from the legacy shell wrappers; deploy-gate keeps the same format.

## Config

- YAML config file (path via `-config`): listen addr, approval timeout,
  deploy endpoints (path → ssh target + remote command + image regex).
- Secrets via env: bot token, webhook HMAC secret, TG callback path
  secret, chat id, thread id, approver IDs.

## Layout

```
main.go                      — wire-up + signal handling
internal/config/             — JSON + env loader
internal/approval/           — pending approvals, reaper
internal/handlers/           — HMAC verify, HTTP handlers, SSH executor, TG-calls
```

## Build + deploy

Cross-compiled locally to `dist/deploy-gate` (linux/amd64, CGO disabled,
stripped). Ansible's `deploy_gate` role copies the binary onto the
metrics host. `make provision` runs `make build-deploy-gate` first;
`go test` is part of that target, so a failing test blocks deploy.

```
make build-deploy-gate    # local cross-compile + tests
make provision            # ansible copies the binary, restarts service
```

The `dist/` directory is gitignored — only source is in version control.

### Promotion path

Current setup assumes a single contributor. When the team grows or the
change frequency rises, the next step is to move builds into GitHub
Actions: each PR builds a tagged binary (or container image), Ansible
fetches a specific tag instead of running `make build-deploy-gate`. That
removes the "trust the developer's local Go version" weak point and
makes releases traceable.
