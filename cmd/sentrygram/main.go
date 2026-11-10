// Command sentrygram relays Sentry issue alerts to a Telegram chat.
//
// Configuration is the environment:
//
//	TELEGRAM_BOT_TOKEN    the bot's token (required)
//	TELEGRAM_CHAT_ID      the chat to post to (required)
//	RELAY_PATH_TOKEN      the last path segment a webhook must carry
//	SENTRY_CLIENT_SECRET  the integration's client secret, to check signatures
//	SENTRY_URL            Sentry's address, for links and the API (https://sentry.io)
//	SENTRY_API_TOKEN      an internal integration token (event:read); adds the latest event's details
//	SENTRY_ORG            the organization slug, when the issue's link does not say it
//	NOISE_CULPRITS        comma-separated culprit prefixes to drop
//	ADDR                  listen address (:8080)
package main

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/rahacloud/sentrygram/internal/relay"
	"github.com/rahacloud/sentrygram/internal/sentry"
	"github.com/rahacloud/sentrygram/internal/telegram"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	if err := run(logger); err != nil {
		logger.Error("stopped", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	token, chat := os.Getenv("TELEGRAM_BOT_TOKEN"), os.Getenv("TELEGRAM_CHAT_ID")
	if token == "" || chat == "" {
		return errors.New("TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID are required")
	}

	var noise []string
	if v := os.Getenv("NOISE_CULPRITS"); v != "" {
		noise = strings.Split(v, ",")
	}

	handler := &relay.Handler{
		Sender:        telegram.New(token, chat),
		PathToken:     os.Getenv("RELAY_PATH_TOKEN"),
		ClientSecret:  os.Getenv("SENTRY_CLIENT_SECRET"),
		SentryURL:     cmp.Or(os.Getenv("SENTRY_URL"), "https://sentry.io"),
		NoiseCulprits: noise,
		Logger:        logger,
	}

	if apiToken := os.Getenv("SENTRY_API_TOKEN"); apiToken != "" {
		handler.Events = sentry.New(handler.SentryURL, apiToken, os.Getenv("SENTRY_ORG"))
	}

	srv := &http.Server{
		Addr:              cmp.Or(os.Getenv("ADDR"), ":8080"),
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errc := make(chan error, 1)

	go func() {
		logger.Info("listening", "addr", srv.Addr, "path_token", handler.PathToken != "", "signature", handler.ClientSecret != "", "event_details", handler.Events != nil)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}

	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	return srv.Shutdown(shutdown)
}
