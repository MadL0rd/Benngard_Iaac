// Package config loads deploy-gate runtime configuration from a JSON file
// (non-sensitive, rendered by Ansible) plus environment variables (secrets,
// supplied via systemd EnvironmentFile).
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Deploy describes one webhook endpoint: how it authenticates, what image
// regex it expects, where it SSHes, and how to invoke the remote script.
type Deploy struct {
	Path         string `json:"path"`           // e.g. "/hooks/deploy-backend-prod"
	Service      string `json:"service"`        // "backend" | "frontend"
	Env          string `json:"env"`            // "dev" | "prod"
	SSHAlias     string `json:"ssh_alias"`      // alias from ssh-config
	RemoteEnvVar string `json:"remote_env_var"` // "BACKEND_IMAGE" | "FRONTEND_IMAGE"
	RemoteScript string `json:"remote_script"` // absolute path on target host
	ImagePattern string `json:"image_pattern"` // regex for the incoming image ref

	// Parsed once at load; never written elsewhere.
	imageRegex *regexp.Regexp `json:"-"`
}

// ImageRegex returns the compiled image regex. Safe to call after Load.
func (deploy *Deploy) ImageRegex() *regexp.Regexp { return deploy.imageRegex }

// Config is the on-disk JSON shape.
type Config struct {
	Listen                 string   `json:"listen"`
	ApprovalTimeoutSeconds int      `json:"approval_timeout_seconds"`
	// TGWebhookBaseURL is the public URL prefix under which /tg/callback
	// is reachable, e.g. "https://metrics.benngard.de/deploy-gate".
	// Concatenated with the literal /tg/callback to form the URL we send
	// to Telegram's setWebhook.
	TGWebhookBaseURL string   `json:"tg_webhook_base_url"`
	Deploys          []Deploy `json:"deploys"`
}

// ApprovalTimeout returns the parsed duration.
func (config *Config) ApprovalTimeout() time.Duration {
	return time.Duration(config.ApprovalTimeoutSeconds) * time.Second
}

// Secrets carries everything that comes via env (never serialized to disk
// outside of /etc/benngard-deploy/deploy-gate.env mode 0640).
type Secrets struct {
	// HMAC SHA256 secret for X-Hub-Signature-256 on /hooks/* endpoints.
	GitHubWebhookSecret string

	// Telegram Bot API token. Shared with grafana-alerts (read-only there).
	TelegramBotToken string

	// Numeric Telegram chat id (groups are negative, e.g. -1001234567890).
	TelegramChatID int64

	// Forum-topic thread id for deploy approvals. 0 = General topic.
	TelegramThreadID int

	// Numeric Telegram user ids allowed to approve/deny.
	ApproverIDs []int64
}

// Load reads the JSON config from path and the secrets from env.
func Load(path string) (*Config, *Secrets, error) {
	rawJSON, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var appConfig Config
	if err := json.Unmarshal(rawJSON, &appConfig); err != nil {
		return nil, nil, fmt.Errorf("parse config: %w", err)
	}
	if appConfig.Listen == "" {
		return nil, nil, fmt.Errorf("listen address must be set")
	}
	if appConfig.ApprovalTimeoutSeconds <= 0 {
		return nil, nil, fmt.Errorf("approval_timeout_seconds must be > 0")
	}
	if appConfig.TGWebhookBaseURL == "" {
		return nil, nil, fmt.Errorf("tg_webhook_base_url must be set")
	}
	if len(appConfig.Deploys) == 0 {
		return nil, nil, fmt.Errorf("at least one deploy must be configured")
	}
	for index := range appConfig.Deploys {
		deploy := &appConfig.Deploys[index]
		if deploy.Path == "" || deploy.Service == "" || deploy.Env == "" ||
			deploy.SSHAlias == "" || deploy.RemoteEnvVar == "" ||
			deploy.RemoteScript == "" || deploy.ImagePattern == "" {
			return nil, nil, fmt.Errorf("deploy #%d: all fields are required", index)
		}
		compiledRegex, err := regexp.Compile(deploy.ImagePattern)
		if err != nil {
			return nil, nil, fmt.Errorf("deploy %s: bad image_pattern: %w", deploy.Path, err)
		}
		deploy.imageRegex = compiledRegex
	}

	secrets, err := loadSecrets()
	if err != nil {
		return nil, nil, err
	}
	return &appConfig, secrets, nil
}

func loadSecrets() (*Secrets, error) {
	secrets := &Secrets{
		GitHubWebhookSecret: os.Getenv("DEPLOY_GATE_GH_WEBHOOK_SECRET"),
		TelegramBotToken:    os.Getenv("DEPLOY_GATE_TG_BOT_TOKEN"),
	}
	required := map[string]string{
		"DEPLOY_GATE_GH_WEBHOOK_SECRET": secrets.GitHubWebhookSecret,
		"DEPLOY_GATE_TG_BOT_TOKEN":      secrets.TelegramBotToken,
	}
	for envName, envValue := range required {
		if envValue == "" {
			return nil, fmt.Errorf("env %s is required", envName)
		}
	}

	rawChatID := strings.TrimSpace(os.Getenv("DEPLOY_GATE_TG_CHAT_ID"))
	if rawChatID == "" {
		return nil, fmt.Errorf("env DEPLOY_GATE_TG_CHAT_ID is required")
	}
	chatID, err := strconv.ParseInt(rawChatID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("DEPLOY_GATE_TG_CHAT_ID %q is not numeric: %w", rawChatID, err)
	}
	secrets.TelegramChatID = chatID

	// Thread is optional — empty means post to the chat's General topic.
	if rawThread := strings.TrimSpace(os.Getenv("DEPLOY_GATE_TG_THREAD_ID")); rawThread != "" {
		threadID, err := strconv.Atoi(rawThread)
		if err != nil {
			return nil, fmt.Errorf("DEPLOY_GATE_TG_THREAD_ID %q is not numeric: %w", rawThread, err)
		}
		secrets.TelegramThreadID = threadID
	}

	approversCSV := strings.TrimSpace(os.Getenv("DEPLOY_GATE_APPROVERS"))
	if approversCSV == "" {
		return nil, fmt.Errorf("env DEPLOY_GATE_APPROVERS is required (CSV of telegram user ids)")
	}
	for _, rawID := range strings.Split(approversCSV, ",") {
		rawID = strings.TrimSpace(rawID)
		if rawID == "" {
			continue
		}
		userID, err := strconv.ParseInt(rawID, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("DEPLOY_GATE_APPROVERS: %q is not a numeric id", rawID)
		}
		secrets.ApproverIDs = append(secrets.ApproverIDs, userID)
	}
	if len(secrets.ApproverIDs) == 0 {
		return nil, fmt.Errorf("DEPLOY_GATE_APPROVERS resolved to empty list")
	}
	return secrets, nil
}

// IsApprover reports whether userID is in the whitelist.
func (secrets *Secrets) IsApprover(userID int64) bool {
	for _, approverID := range secrets.ApproverIDs {
		if approverID == userID {
			return true
		}
	}
	return false
}
