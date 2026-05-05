// Package handlers wires Config + Secrets + Store + Bot into HTTP
// handlers and the TG callback flow.
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"benngard/deploy-gate/internal/approval"
	"benngard/deploy-gate/internal/config"
)

// tgCallTimeout — TG ops use background ctx (must land even after the
// caller's ctx ends, e.g. GH disconnects right after our 202).
const tgCallTimeout = 10 * time.Second

// SSHExecutor is an interface so tests can stub the SSH transport.
type SSHExecutor interface {
	Run(ctx context.Context, alias, remoteCommand string) (combinedOutput string, err error)
}

type RealSSH struct {
	SSHConfigPath string
}

func (realSSH *RealSSH) Run(ctx context.Context, alias, remoteCommand string) (string, error) {
	command := exec.CommandContext(ctx, "ssh", "-F", realSSH.SSHConfigPath, alias, remoteCommand)
	output, err := command.CombinedOutput()
	return string(output), err
}

type Server struct {
	Config  *config.Config
	Secrets *config.Secrets
	Store   *approval.PendingStore
	Bot     *bot.Bot
	SSH     SSHExecutor
	Workers sync.WaitGroup
}

func (server *Server) Register(mux *http.ServeMux, tgCallbackPath string) {
	for index := range server.Config.Deploys {
		deploy := &server.Config.Deploys[index]
		mux.HandleFunc("POST "+deploy.Path, server.deployHandler(deploy))
	}
	mux.HandleFunc("POST "+tgCallbackPath, server.Bot.WebhookHandler())
	mux.HandleFunc("GET /health", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(writer, "ok\n")
	})
}

// hookPayload — only the field we care about from GH's webhook body.
type hookPayload struct {
	Image string `json:"image"`
}

func (server *Server) deployHandler(deploy *config.Deploy) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		log := slog.With("service", deploy.Service, "env", deploy.Env)

		body, err := io.ReadAll(io.LimitReader(request.Body, 64*1024))
		if err != nil {
			http.Error(writer, "read body", http.StatusBadRequest)
			return
		}

		signature := request.Header.Get("X-Hub-Signature-256")
		if !verifyHMAC(signature, server.Secrets.GitHubWebhookSecret, body) {
			server.rejectInvalid(writer, deploy, "hmac", "", request.RemoteAddr,
				http.StatusUnauthorized, "bad signature")
			return
		}

		var payload hookPayload
		if err := json.Unmarshal(body, &payload); err != nil {
			server.rejectInvalid(writer, deploy, "bad-json", "", request.RemoteAddr,
				http.StatusBadRequest, "bad json")
			return
		}
		if !deploy.ImageRegex().MatchString(payload.Image) {
			server.rejectInvalid(writer, deploy, "bad-image", payload.Image, request.RemoteAddr,
				http.StatusBadRequest, "bad image")
			return
		}

		pending := server.Store.Add(&approval.Pending{
			Service: deploy.Service,
			Env:     deploy.Env,
			Image:   payload.Image,
		})
		log.Info("deploy pending approval",
			"event", "pending",
			"image", payload.Image,
			"id", pending.ID)

		buttons := [][]models.InlineKeyboardButton{{
			{Text: "✅ Approve", CallbackData: "approve:" + pending.ID},
			{Text: "❌ Deny", CallbackData: "deny:" + pending.ID},
		}}

		tgCtx, cancel := context.WithTimeout(context.Background(), tgCallTimeout)
		sentMessage, err := server.Bot.SendMessage(tgCtx, &bot.SendMessageParams{
			ChatID:              server.Secrets.TelegramChatID,
			MessageThreadID:     server.Secrets.TelegramThreadID,
			Text:                pendingMessage(pending),
			DisableNotification: deploy.Env == "dev",
			LinkPreviewOptions: &models.LinkPreviewOptions{
				IsDisabled: bot.True(),
			},
			ReplyMarkup: models.InlineKeyboardMarkup{
				InlineKeyboard: buttons,
			},
		})
		cancel()
		if err != nil {
			// TG unreachable → approval never comes. Resolve immediately so
			// the reaper doesn't fire another timeout for nothing.
			server.Store.Resolve(pending.ID)
			log.Error("deploy failed: telegram unavailable",
				"event", "FAILED",
				"image", payload.Image,
				"reason", "telegram-down",
				"err", err)
			http.Error(writer, "telegram unavailable", http.StatusBadGateway)
			return
		}
		server.Store.SetMessageID(pending.ID, sentMessage.ID)

		writer.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprintf(writer, "queued id=%s\n", pending.ID)
	}
}

func (server *Server) rejectInvalid(
	writer http.ResponseWriter,
	deploy *config.Deploy,
	reason, image, remote string,
	httpStatus int, httpMessage string,
) {
	attrs := []any{
		"event", "invalid",
		"service", deploy.Service,
		"env", deploy.Env,
		"reason", reason,
		"remote", remote,
	}
	if image != "" {
		attrs = append(attrs, "image", image)
	}
	slog.Warn("invalid webhook request", attrs...)
	server.notifyInvalid(deploy, reason, image, remote)
	http.Error(writer, httpMessage, httpStatus)
}

// HandleCallback — registered with bot.RegisterHandlerMatchFunc, called
// by a library worker for every callback_query update.
func (server *Server) HandleCallback(_ context.Context, _ *bot.Bot, update *models.Update) {
	callback := update.CallbackQuery

	tgCtx, cancel := context.WithTimeout(context.Background(), tgCallTimeout)
	defer cancel()

	if !server.Secrets.IsApprover(callback.From.ID) {
		slog.Warn("unauthorized callback",
			"user_id", callback.From.ID,
			"username", callback.From.Username)
		_, _ = server.Bot.AnswerCallbackQuery(tgCtx, &bot.AnswerCallbackQueryParams{
			CallbackQueryID: callback.ID,
			Text:            "Not authorized.",
			ShowAlert:       true,
		})
		return
	}

	// callback_data is "approve:<id>" or "deny:<id>".
	action, pendingID, ok := strings.Cut(callback.Data, ":")
	if !ok || (action != "approve" && action != "deny") {
		slog.Warn("malformed callback_data", "data", callback.Data)
		_, _ = server.Bot.AnswerCallbackQuery(tgCtx, &bot.AnswerCallbackQueryParams{
			CallbackQueryID: callback.ID,
			Text:            "Bad data.",
		})
		return
	}

	pending, ok := server.Store.Resolve(pendingID)
	if !ok {
		_, _ = server.Bot.AnswerCallbackQuery(tgCtx, &bot.AnswerCallbackQueryParams{
			CallbackQueryID: callback.ID,
			Text:            "Already resolved or expired.",
			ShowAlert:       true,
		})
		return
	}
	approvedAt := time.Now().UTC()

	log := slog.With(
		"service", pending.Service,
		"env", pending.Env,
		"image", pending.Image,
		"id", pending.ID,
		"by_user_id", callback.From.ID,
		"by_user_label", UserLabel(callback.From),
	)

	// Dismiss spinner immediately so user UI doesn't hang while we ssh.
	_, _ = server.Bot.AnswerCallbackQuery(tgCtx, &bot.AnswerCallbackQueryParams{
		CallbackQueryID: callback.ID,
	})

	if action == "deny" {
		log.Info("deploy denied", "event", "denied")
		server.editMessageInplace(tgCtx, pending.TGMessageID, deniedMessage(pending, callback.From))
		return
	}

	log.Info("deploy approved", "event", "approved")
	server.editMessageInplace(tgCtx, pending.TGMessageID, deployingMessage(pending, callback.From, approvedAt))

	server.Workers.Add(1)
	go func() {
		defer server.Workers.Done()
		server.runDeploy(pending, callback.From, approvedAt)
	}()
}

// RetrySetWebhook — exponential backoff until success or ctx cancel.
// Background-only: a fatal here would restart the process, which would
// generate a new ephemeral secret-token and leave TG with a stale one.
func (server *Server) RetrySetWebhook(ctx context.Context, url, secretToken string) {
	backoff := 2 * time.Second
	const maxBackoff = 2 * time.Minute

	for {
		attemptCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		_, err := server.Bot.SetWebhook(attemptCtx, &bot.SetWebhookParams{
			URL:                url,
			SecretToken:        secretToken,
			AllowedUpdates:     []string{"callback_query"},
			DropPendingUpdates: true,
			MaxConnections:     4,
		})
		cancel()
		if err == nil {
			slog.Info("telegram webhook registered", "event", "startup")
			return
		}

		slog.Warn("telegram setWebhook failed, will retry",
			"err", err,
			"backoff", backoff.String())

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}

		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func (server *Server) runDeploy(pending *approval.Pending, approver models.User, approvedAt time.Time) {
	log := slog.With(
		"service", pending.Service,
		"env", pending.Env,
		"image", pending.Image,
		"id", pending.ID,
	)

	deploy, ok := server.findDeploy(pending.Service, pending.Env)
	if !ok {
		// Should be impossible: pendings come from deployHandler which only
		// fires for known deploys. Config drift means deploys[] changed
		// between Add() and Resolve() — bail loudly.
		log.Error("deploy failed: config drift", "event", "FAILED", "reason", "config-drift")
		server.editMessageBestEffort(pending.TGMessageID,
			doneMessage(pending, approver, approvedAt, time.Now().UTC(), "config-drift: deploy not in catalog", true))
		return
	}

	remoteCommand := fmt.Sprintf("%s=%s %s", deploy.RemoteEnvVar, pending.Image, deploy.RemoteScript)
	log.Info("deploy starting", "event", "start")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	output, err := server.SSH.Run(ctx, deploy.SSHAlias, remoteCommand)
	finishedAt := time.Now().UTC()

	if err != nil {
		log.Error("deploy failed", "event", "FAILED", "err", err)
		server.editMessageBestEffort(pending.TGMessageID,
			doneMessage(pending, approver, approvedAt, finishedAt,
				tail(output, 20)+"\n["+err.Error()+"]", true))
		return
	}
	log.Info("deploy ok", "event", "ok")
	server.editMessageBestEffort(pending.TGMessageID,
		doneMessage(pending, approver, approvedAt, finishedAt, "deploy ok", false))
}

// notifyInvalid — fire-and-log: TG failure logs but the HTTP response is
// already gone, so retry isn't meaningful.
func (server *Server) notifyInvalid(deploy *config.Deploy, reason, image, remote string) {
	ctx, cancel := context.WithTimeout(context.Background(), tgCallTimeout)
	defer cancel()
	_, err := server.Bot.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:              server.Secrets.TelegramChatID,
		MessageThreadID:     server.Secrets.TelegramThreadID,
		Text:                invalidMessage(deploy.Service, deploy.Env, reason, image, remote),
		DisableNotification: deploy.Env == "dev",
		LinkPreviewOptions: &models.LinkPreviewOptions{
			IsDisabled: bot.True(),
		},
	})
	if err != nil {
		slog.Warn("notifyInvalid: telegram send failed",
			"service", deploy.Service,
			"env", deploy.Env,
			"reason", reason,
			"err", err)
	}
}

// editMessageInplace — uses a caller-supplied ctx. Edit errors don't fail
// the flow: stale buttons are annoying, not broken.
func (server *Server) editMessageInplace(ctx context.Context, messageID int, text string) {
	_, err := server.Bot.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID:    server.Secrets.TelegramChatID,
		MessageID: messageID,
		Text:      text,
		ReplyMarkup: models.InlineKeyboardMarkup{
			InlineKeyboard: [][]models.InlineKeyboardButton{},
		},
	})
	if err != nil && !isNotModified(err) {
		slog.Warn("telegram edit message failed", "message_id", messageID, "err", err)
	}
}

// editMessageBestEffort — for background goroutines (runDeploy, ReapLoop)
// that don't carry an inbound ctx of their own.
func (server *Server) editMessageBestEffort(messageID int, text string) {
	ctx, cancel := context.WithTimeout(context.Background(), tgCallTimeout)
	defer cancel()
	server.editMessageInplace(ctx, messageID, text)
}

func (server *Server) findDeploy(service, env string) (*config.Deploy, bool) {
	for index := range server.Config.Deploys {
		deploy := &server.Config.Deploys[index]
		if deploy.Service == service && deploy.Env == env {
			return deploy, true
		}
	}
	return nil, false
}

func (server *Server) ReapLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			expired := server.Store.Expired(now)
			for _, pending := range expired {
				slog.Info("deploy timed out (auto-deny)",
					"event", "timeout",
					"service", pending.Service,
					"env", pending.Env,
					"image", pending.Image,
					"id", pending.ID)
				server.editMessageBestEffort(pending.TGMessageID, timeoutMessage(pending))
			}
			server.Store.Sweep(now)
		}
	}
}
