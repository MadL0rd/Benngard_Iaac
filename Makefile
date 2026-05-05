SHELL := /bin/bash

# Force ansible-playbook to read our checked-in ansible.cfg even when the
# user runs `make ...` from the repo root. Without this Ansible only
# discovers cfg in cwd, $HOME, or /etc/ansible/.
export ANSIBLE_CONFIG := ansible/ansible.cfg

# Vault password comes from ansible.cfg (vault_password_file = ../.vault_pass).
# Use --ask-vault-pass only when .vault_pass is missing or for a one-off override.
ANSIBLE       := ansible-playbook -i ansible/inventory/hosts.ini
ANSIBLE_ADHOC := ansible          -i ansible/inventory/hosts.ini
ANSIBLE_OPTS  ?=

.DEFAULT_GOAL := help

# ── Help ──────────────────────────────────────────────────────────────────────

.PHONY: help
help: ## List commands
	@awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z_-]+:.*##/ { printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)
	@echo ""
	@echo "  Limit provision scope:  make provision ANSIBLE_OPTS='--limit app_servers'"
	@echo "  SSH/logs/status:        ssh dev.benngard.de   etc."

# ── Vault ─────────────────────────────────────────────────────────────────────
#
# Convention: each encrypted <file> (committed) has a sibling plaintext
# <file>.plain (gitignored — `*.plain` is in .gitignore to prevent accidental
# `git add` of secrets).
#
# Workflow:
#   make vault-init      # create missing <file>.plain from templates + autofill
#   $EDITOR <file>.plain # fill in remaining CHANGE_ME values by hand
#   make vault-encrypt   # <file>.plain → <file> (for every pair with .plain)
#   git add <file>       # only encrypted ever gets committed
#   ...
#   make vault-decrypt   # <file> → <file>.plain (to edit secrets)

# Encrypted ↔ plain pairs — consumed by vault-encrypt / vault-decrypt.
VAULT_FILES := \
  ansible/group_vars/all/vault.yml \
  ansible/envs/.env.api.dev \
  ansible/envs/.env.api.prod

# (template → plain target) pairs for vault-init. One template can feed
# multiple targets (e.g. .env.api.example used for both dev and prod).
# Format "src:dst", colon-separated.
VAULT_INIT_PAIRS := \
  ansible/group_vars/all/vault.yml.plain.example:ansible/group_vars/all/vault.yml.plain \
  ansible/envs/.env.api.example:ansible/envs/.env.api.dev.plain \
  ansible/envs/.env.api.example:ansible/envs/.env.api.prod.plain

.PHONY: vault-init
vault-init: ## Create missing <file>.plain from templates, autofill CHANGE_ME_PASSWORD
	@for pair in $(VAULT_INIT_PAIRS); do \
		src=$${pair%%:*}; dst=$${pair##*:}; \
		if [ -f "$$dst" ]; then echo "  skip $$dst (already exists)"; continue; fi; \
		if [ ! -f "$$src" ]; then echo "❌  template $$src not found"; exit 1; fi; \
		cp "$$src" "$$dst"; \
		echo "  create $$dst (from $$src)"; \
		while grep -q CHANGE_ME_PASSWORD "$$dst"; do \
			pw=$$(openssl rand -hex 16); \
			awk -v pw="$$pw" '!done && /CHANGE_ME_PASSWORD/ { sub(/CHANGE_ME_PASSWORD/, pw); done=1 } 1' \
				"$$dst" > "$$dst.tmp" && mv "$$dst.tmp" "$$dst"; \
		done; \
	done
	@echo ""
	@todo=0; for pair in $(VAULT_INIT_PAIRS); do \
		dst=$${pair##*:}; \
		if [ -f "$$dst" ] && grep -q CHANGE_ME "$$dst"; then \
			if [ "$$todo" -eq 0 ]; then echo "⚠️   Fill in by hand:"; todo=1; fi; \
			echo "  $$dst:"; grep -n CHANGE_ME "$$dst" | sed 's/^/    /'; \
		fi; \
	done; \
	if [ "$$todo" -eq 0 ]; then echo "✅  All filled in."; fi
	@echo ""
	@echo "Next: \$$EDITOR <plain-file> + make vault-encrypt"

.PHONY: vault-encrypt
vault-encrypt: ## Encrypt <file>.plain → <file> for each pair in VAULT_FILES
	@for f in $(VAULT_FILES); do \
		plain="$$f.plain"; \
		if [ ! -f "$$plain" ]; then echo "  skip $$f (no $$plain)"; continue; fi; \
		if grep -q 'CHANGE_ME' "$$plain"; then \
			echo "❌  $$plain still has CHANGE_ME markers:"; \
			grep -n 'CHANGE_ME' "$$plain"; exit 1; \
		fi; \
		echo "  encrypt $$plain → $$f"; \
		ansible-vault encrypt "$$plain" --output "$$f"; \
	done

.PHONY: vault-decrypt
vault-decrypt: ## Decrypt <file> → <file>.plain for each pair in VAULT_FILES
	@for f in $(VAULT_FILES); do \
		if [ ! -f "$$f" ]; then echo "  skip $$f (no encrypted file)"; continue; fi; \
		echo "  decrypt $$f → $$f.plain"; \
		ansible-vault decrypt "$$f" --output "$$f.plain"; \
	done

# ── Infrastructure (Ansible) ──────────────────────────────────────────────────

.PHONY: bootstrap
bootstrap: ## First-time setup of dev/prod/metrics (once, as rockylinux user)
	$(ANSIBLE) ansible/bootstrap.yml $(ANSIBLE_OPTS)

.PHONY: build-deploy-gate
build-deploy-gate: ## Cross-compile deploy-gate to deploy-gate/dist/ (runs tests first)
	cd deploy-gate && go test ./... && \
		GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
		go build -trimpath -ldflags='-s -w' -o dist/deploy-gate .

.PHONY: provision
provision: build-deploy-gate ## Full infra provision (as madlord, idempotent)
	$(ANSIBLE) ansible/provision.yml $(ANSIBLE_OPTS)

.PHONY: provision-metrics
provision-metrics: build-deploy-gate ## Provision metrics host only (deploy-gate + monitoring stack)
	$(ANSIBLE) ansible/provision.yml --limit metrics_server $(ANSIBLE_OPTS)

# ── VPN (dedicated single-purpose vpn host) ───────────────────────────────────

.PHONY: bootstrap-vpn
bootstrap-vpn: ## First-time setup of vpn host (once, as rockylinux user)
	$(ANSIBLE) ansible/bootstrap-vpn.yml $(ANSIBLE_OPTS)

.PHONY: provision-vpn
provision-vpn: ## Deploy / update WireGuard on vpn host
	$(ANSIBLE) ansible/wireguard.yml $(ANSIBLE_OPTS)

# ── App deploy (backend / frontend infra on app servers) ──────────────────────

.PHONY: deploy-app
deploy-app: ## Run app.yml: env + compose + deploy.sh + docker login on app servers
	$(ANSIBLE) ansible/app.yml $(ANSIBLE_OPTS)

.PHONY: deploy-app-env
deploy-app-env: ## Sync only .env to app servers (tagged run of app.yml)
	$(ANSIBLE) ansible/app.yml --tags env $(ANSIBLE_OPTS)

.PHONY: rescue-abi
rescue-abi: ## Emergency: dnf upgrade critical libs via rockylinux+raw (when madlord sudo is broken)
	ansible -i ansible/inventory/hosts.ini all_servers \
		--user rockylinux \
		--private-key ~/.ssh/benngard-default-exoscale \
		--become \
		--module-name raw \
		--args 'dnf upgrade -y openssl openssl-libs systemd systemd-libs glibc nss libxcrypt' \
		$(ANSIBLE_OPTS)

# ── Canary auto-reboot — emergency overrides ──────────────────────────────────

.PHONY: disable-canary-reboot
disable-canary-reboot: ## Urgent: disable auto-reboot.timer on prod + metrics
	$(ANSIBLE_ADHOC) 'prod_app:metrics_server' -b \
		-m systemd -a 'name=auto-reboot.timer state=stopped enabled=false'

.PHONY: enable-canary-reboot
enable-canary-reboot: ## Re-enable auto-reboot.timer on prod + metrics
	$(ANSIBLE_ADHOC) 'prod_app:metrics_server' -b \
		-m systemd -a 'name=auto-reboot.timer state=started enabled=true'

# ── Grafana: dashboards + alerting ────────────────────────────────────────────

METRICS_HOST      := metrics.benngard.de
METRICS_USER      := madlord
METRICS_SSH_KEY   := ~/.ssh/benngard-madlord-exoscale
METRICS_SSH       := ssh -i $(METRICS_SSH_KEY)
METRICS_TARGET    := $(METRICS_USER)@$(METRICS_HOST)
DASHBOARDS_LOCAL  := infra/metrics/grafana/dashboards/
DASHBOARDS_REMOTE := /opt/metrics-stack/grafana/dashboards/
ALERTING_LOCAL    := infra/metrics/grafana/provisioning/alerting/
ALERTING_REMOTE   := /opt/metrics-stack/grafana/provisioning/alerting/
GRAFANA_API       := https://$(METRICS_HOST)/api

# contactpoints.yml deliberately skipped: contains a Telegram bot token from
# vault, rendered by Ansible (roles/metrics/templates/grafana-contactpoints.yml.j2).

.PHONY: sync-grafana
sync-grafana: ## Push dashboards + alerting (rules/policies/templates) to metrics
	# Dashboards — Grafana's file watcher picks them up in ~30s, no restart.
	rsync -avz --checksum -e '$(METRICS_SSH)' \
		$(DASHBOARDS_LOCAL) $(METRICS_TARGET):$(DASHBOARDS_REMOTE)
	$(METRICS_SSH) $(METRICS_TARGET) "chmod 644 $(DASHBOARDS_REMOTE)*.json"
	# Alerting — rules/policies/templates (no contactpoints, see above).
	rsync -avz --checksum -e '$(METRICS_SSH)' \
		$(ALERTING_LOCAL)rules.yml \
		$(ALERTING_LOCAL)policies.yml \
		$(ALERTING_LOCAL)templates.yml \
		$(METRICS_TARGET):$(ALERTING_REMOTE)
	# Grafana has no file watcher for alerting → restart container.
	$(METRICS_SSH) $(METRICS_TARGET) 'docker restart grafana'
	@echo ""
	@echo "✅  Synced. Grafana restarted (~5s downtime)."

.PHONY: pull-grafana
pull-grafana: ## Pull dashboards + alerting from Grafana into repo (export GRAFANA_PASS=xxx)
	@test -n "$$GRAFANA_PASS" || { echo "❌  Set: export GRAFANA_PASS=<password>"; exit 1; }
	@command -v jq >/dev/null || { echo "❌  jq required"; exit 1; }
	@echo "Pulling dashboards..."
	@curl -sf -u "admin:$$GRAFANA_PASS" "$(GRAFANA_API)/search?type=dash-db" \
		| jq -r '.[] | "\(.uid) \(.title)"' \
		| while IFS=' ' read -r uid title; do \
			echo "  ← $$title ($$uid)"; \
			curl -sf -u "admin:$$GRAFANA_PASS" "$(GRAFANA_API)/dashboards/uid/$$uid" \
				| jq '.dashboard | del(.id) | .version = 1' \
				> $(DASHBOARDS_LOCAL)$$uid.json; \
		done
	@echo "Pulling alerting (rules + policies)..."
	@curl -sf -u "admin:$$GRAFANA_PASS" \
		"$(GRAFANA_API)/v1/provisioning/alert-rules/export?format=yaml" \
		> $(ALERTING_LOCAL)rules.yml
	@curl -sf -u "admin:$$GRAFANA_PASS" \
		"$(GRAFANA_API)/v1/provisioning/policies/export?format=yaml" \
		> $(ALERTING_LOCAL)policies.yml
	@# templates: no /export endpoint — source of truth is git.
	@# contactpoints: contain secrets — source of truth is vault.
	@echo ""
	@echo "Done. Review: git diff infra/metrics/grafana/"

# ── WireGuard VPN ─────────────────────────────────────────────────────────────

# Peer list must match wireguard_peers in vpn_server.yml — update both when
# adding a peer.
WG_PEERS   := anton_tek mkibardin andreas_belo
WG_DIR     := wireguard/keys
WG_PORT    := 51820
WG_SUBNET  := 10.10.0.0/24

# IPs from inventory. vpn = tunnel endpoint; dev/metrics = transit destinations
# the client reaches via the vpn host.
VPN_IP     := $(shell awk '$$1 == "vpn"     { sub(/.*ansible_host=/, "", $$2); print $$2 }' ansible/inventory/hosts.ini)
METRICS_IP := $(shell awk '$$1 == "metrics" { sub(/.*ansible_host=/, "", $$2); print $$2 }' ansible/inventory/hosts.ini)
DEV_IP     := $(shell awk '$$1 == "dev"     { sub(/.*ansible_host=/, "", $$2); print $$2 }' ansible/inventory/hosts.ini)

.PHONY: wg-keygen
wg-keygen: ## Generate WireGuard keys for all peers (skips existing)
	@mkdir -p $(WG_DIR) && chmod 700 $(WG_DIR)
	@for user in $(WG_PEERS); do \
		if [ -f $(WG_DIR)/$$user.key ]; then \
			echo "  skip $$user (already exists)"; \
		else \
			wg genkey | tee $(WG_DIR)/$$user.key | wg pubkey > $(WG_DIR)/$$user.pub; \
			chmod 600 $(WG_DIR)/$$user.key; \
			echo "  ok   $$user → $(WG_DIR)/$$user.key / $$user.pub"; \
		fi \
	done

.PHONY: wg-pubkeys
wg-pubkeys: ## Print peer public keys (for pasting into vpn_server.yml)
	@for user in $(WG_PEERS); do \
		printf "%-16s %s\n" "$$user" "$$(cat $(WG_DIR)/$$user.pub 2>/dev/null || echo '— key missing, run make wg-keygen')"; \
	done

.PHONY: wg-configs
wg-configs: ## Generate client .conf files (wireguard/keys/<user>.conf)
	@test -f $(WG_DIR)/wg-server.pub || { echo "❌  wg-server.pub not found — run make provision-vpn"; exit 1; }
	@for entry in "anton_tek:10.10.0.2" "mkibardin:10.10.0.3" "andreas_belo:10.10.0.4"; do \
		user=$${entry%%:*}; vpn_ip=$${entry##*:}; \
		if [ ! -f $(WG_DIR)/$$user.key ]; then \
			echo "  skip $$user (no key, run make wg-keygen)"; continue; \
		fi; \
		printf '[Interface]\nAddress    = %s/32\nPrivateKey = %s\n\n[Peer]\nPublicKey           = %s\nEndpoint            = %s:$(WG_PORT)\nAllowedIPs          = $(WG_SUBNET), %s/32, %s/32\nPersistentKeepalive = 25\n' \
			"$$vpn_ip" \
			"$$(cat $(WG_DIR)/$$user.key)" \
			"$$(cat $(WG_DIR)/wg-server.pub)" \
			"$(VPN_IP)" \
			"$(DEV_IP)" \
			"$(METRICS_IP)" \
			> $(WG_DIR)/$$user.conf; \
		echo "  ok   $(WG_DIR)/$$user.conf"; \
	done
