// Command deploy-gate is the CI/CD orchestrator that gates every release
// behind a Telegram out-of-band approval. See ./README.md for the threat
// model and full flow.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"benngard/deploy-gate/internal/approval"
	"benngard/deploy-gate/internal/config"
	"benngard/deploy-gate/internal/handlers"
)

const tgCallbackPath = "/tg/callback"

func main() {
	configPath := flag.String("config", "/etc/benngard-deploy/config.json", "config JSON path")
	sshConfigPath := flag.String("ssh-config", "/etc/benngard-deploy/ssh-config", "ssh-config path")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	appConfig, secrets, err := config.Load(*configPath)
	if err != nil {
		fatal("config load failed", "err", err)
	}

	secretToken := newSecretToken()

	telegramBot, err := bot.New(
		secrets.TelegramBotToken,
		bot.WithWebhookSecretToken(secretToken),
	)
	if err != nil {
		fatal("telegram init failed", "err", err)
	}

	pendingStore := approval.NewStore(appConfig.ApprovalTimeout())
	gateServer := &handlers.Server{
		Config:  appConfig,
		Secrets: secrets,
		Store:   pendingStore,
		Bot:     telegramBot,
		SSH:     &handlers.RealSSH{SSHConfigPath: *sshConfigPath},
	}

	telegramBot.RegisterHandlerMatchFunc(
		func(update *models.Update) bool { return update.CallbackQuery != nil },
		gateServer.HandleCallback,
	)

	mux := http.NewServeMux()
	gateServer.Register(mux, tgCallbackPath)

	httpServer := &http.Server{
		Addr:              appConfig.Listen,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	go gateServer.ReapLoop(ctx)
	go telegramBot.StartWebhook(ctx)

	go func() {
		slog.Info("listening", "event", "startup", "addr", appConfig.Listen)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fatal("listen failed", "event", "startup-error", "err", err)
		}
	}()

	go gateServer.RetrySetWebhook(ctx, appConfig.TGWebhookBaseURL+tgCallbackPath, secretToken)

	<-ctx.Done()
	drainAndShutdown(httpServer, &gateServer.Workers)
}

func drainAndShutdown(httpServer *http.Server, workers *sync.WaitGroup) {
	slog.Info("signal received, draining", "event", "shutdown")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		slog.Warn("http shutdown error", "event", "shutdown", "err", err)
	}
	workers.Wait()
	slog.Info("bye", "event", "shutdown")
}

// newSecretToken — 256 bits, URL-safe. Only this process and Telegram
// ever know it.
func newSecretToken() string {
	var randomBytes [32]byte
	if _, err := rand.Read(randomBytes[:]); err != nil {
		panic("crypto/rand: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(randomBytes[:])
}

func fatal(message string, attrs ...any) {
	slog.Error(message, attrs...)
	os.Exit(1)
}
