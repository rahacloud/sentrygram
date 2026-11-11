package relay

import (
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// maxBody bounds a webhook body; Sentry's issue payloads are a few kilobytes.
const maxBody = 1 << 20

// Sender delivers one message; the Telegram client is the real one.
//
// An error with a Temporary() bool method that reports false is not retried,
// and one with a RetryAfter() time.Duration method sets the wait before the
// next attempt.
type Sender interface {
	Send(ctx context.Context, text string) error
}

// EventSource reads an issue's latest event; Sentry's API client is the real one.
type EventSource interface {
	LatestEvent(ctx context.Context, issue Issue) (*Event, error)
}

const (
	// eventTimeout bounds the read of an event, so a slow Sentry delays a message
	// by at most this much; the message goes out without the details then.
	eventTimeout = 5 * time.Second
	// deliverTimeout bounds everything one message may take, retries included.
	deliverTimeout = 2 * time.Minute
	// defaultAttempts and defaultRetryDelay apply when the Handler leaves them zero.
	defaultAttempts   = 4
	defaultRetryDelay = 2 * time.Second
)

// Handler answers Sentry's integration webhooks.
//
// Sentry waits only one second for an answer and retries a request that took
// longer, so the handler answers as soon as a webhook is checked and delivers
// the message in the background, retrying Telegram itself.
type Handler struct {
	Sender Sender
	// Events, when set, adds the latest event's details to each message.
	Events EventSource
	// PathToken, when set, must be the last segment of the request path.
	// Use it when the relay is reachable from the internet.
	PathToken string
	// ClientSecret, when set, checks Sentry-Hook-Signature (HMAC-SHA256 of the body).
	ClientSecret string
	// SentryURL builds an issue's link when the payload carries none.
	SentryURL string
	// NoiseCulprits drops issues whose culprit starts with one of these.
	NoiseCulprits []string
	// Attempts is how many times a message is sent before it is given up; 4 when zero.
	Attempts int
	// RetryDelay is the wait before the first retry, doubled after each; 2s when zero.
	RetryDelay time.Duration
	Logger     *slog.Logger

	inflight sync.WaitGroup
}

type webhook struct {
	Action Action `json:"action"`
	Data   struct {
		Issue Issue `json:"issue"`
	} `json:"data"`
}

func (h *Handler) authorized(r *http.Request, body []byte) bool {
	if h.PathToken != "" {
		segment := strings.TrimRight(r.URL.Path, "/")
		segment = segment[strings.LastIndex(segment, "/")+1:]

		if subtle.ConstantTimeCompare([]byte(segment), []byte(h.PathToken)) != 1 {
			return false
		}
	}

	if h.ClientSecret != "" {
		mac := hmac.New(sha256.New, []byte(h.ClientSecret))
		mac.Write(body)

		if !hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(r.Header.Get("Sentry-Hook-Signature"))) {
			return false
		}
	}

	return true
}

// ServeHTTP answers GET with 200 (a health check). An issue webhook that is
// posted gets 202 and its message is delivered in the background; one that is
// dropped gets 200.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)

		return
	}

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)

		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)

		return
	}

	if !h.authorized(r, body) {
		h.Logger.WarnContext(r.Context(), "refused an unauthorized request", "remote", r.RemoteAddr)
		w.WriteHeader(http.StatusNotFound)

		return
	}

	var hook webhook
	if err := json.Unmarshal(body, &hook); err != nil {
		w.WriteHeader(http.StatusBadRequest)

		return
	}

	issue := hook.Data.Issue

	if r.Header.Get("Sentry-Hook-Resource") != "issue" || (hook.Action != Created && hook.Action != Unresolved) {
		w.WriteHeader(http.StatusOK)

		return
	}

	for _, noise := range h.NoiseCulprits {
		if noise != "" && strings.HasPrefix(issue.Culprit, noise) {
			h.Logger.InfoContext(r.Context(), "dropped noise", "issue", issue.ShortID)
			w.WriteHeader(http.StatusOK)

			return
		}
	}

	// The request's context ends when Sentry hangs up; the delivery must outlive it.
	ctx := context.WithoutCancel(r.Context())

	h.inflight.Go(func() { h.deliver(ctx, hook.Action, issue) })
	w.WriteHeader(http.StatusAccepted)
}

// Drain waits for the messages still being delivered, or until ctx ends.
// Call it after the server has stopped taking requests.
func (h *Handler) Drain(ctx context.Context) error {
	done := make(chan struct{})

	go func() {
		h.inflight.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type temporary interface{ Temporary() bool }

type retryAfter interface{ RetryAfter() time.Duration }

func (h *Handler) deliver(ctx context.Context, action Action, issue Issue) {
	ctx, cancel := context.WithTimeout(ctx, deliverTimeout)
	defer cancel()

	text := Message(issue, action, h.event(ctx, issue), h.SentryURL)
	attempts := cmp.Or(h.Attempts, defaultAttempts)
	delay := cmp.Or(h.RetryDelay, defaultRetryDelay)

	for attempt := 1; ; attempt++ {
		err := h.Sender.Send(ctx, text)
		if err == nil {
			h.Logger.InfoContext(ctx, "posted", "action", action, "issue", issue.ShortID, "attempt", attempt)

			return
		}

		var t temporary
		if attempt >= attempts || (errors.As(err, &t) && !t.Temporary()) {
			h.Logger.ErrorContext(ctx, "telegram failed, message lost", "issue", issue.ShortID, "attempts", attempt, "err", err)

			return
		}

		wait := delay

		var ra retryAfter
		if errors.As(err, &ra) && ra.RetryAfter() > wait {
			wait = ra.RetryAfter()
		}

		h.Logger.WarnContext(ctx, "telegram failed, retrying", "issue", issue.ShortID, "attempt", attempt, "wait", wait, "err", err)

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			h.Logger.ErrorContext(ctx, "telegram failed, message lost", "issue", issue.ShortID, "attempts", attempt, "err", ctx.Err())

			return
		case <-timer.C:
		}

		delay *= 2
	}
}

func (h *Handler) event(ctx context.Context, issue Issue) *Event {
	if h.Events == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, eventTimeout)
	defer cancel()

	ev, err := h.Events.LatestEvent(ctx, issue)
	if err != nil {
		h.Logger.WarnContext(ctx, "posting without the event's details", "issue", issue.ShortID, "err", err)

		return nil
	}

	return ev
}
