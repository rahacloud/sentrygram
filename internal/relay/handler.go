package relay

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// maxBody bounds a webhook body; Sentry's issue payloads are a few kilobytes.
const maxBody = 1 << 20

// Sender delivers one message; the Telegram client is the real one.
type Sender interface {
	Send(ctx context.Context, text string) error
}

// EventSource reads an issue's latest event; Sentry's API client is the real one.
type EventSource interface {
	LatestEvent(ctx context.Context, issue Issue) (*Event, error)
}

// eventTimeout bounds the read of an event, so a slow Sentry delays a message
// by at most this much; the message goes out without the details then.
const eventTimeout = 5 * time.Second

// Handler answers Sentry's integration webhooks.
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
	Logger        *slog.Logger
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

// ServeHTTP answers GET with 200 (a health check) and posts issue webhooks.
// Sentry retries on 5xx, so only a failed delivery to Telegram answers 502.
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

	if err := h.Sender.Send(r.Context(), Message(issue, hook.Action, h.event(r.Context(), issue), h.SentryURL)); err != nil {
		h.Logger.ErrorContext(r.Context(), "telegram failed", "issue", issue.ShortID, "err", err)
		w.WriteHeader(http.StatusBadGateway)

		return
	}

	h.Logger.InfoContext(r.Context(), "posted", "action", hook.Action, "issue", issue.ShortID)
	w.WriteHeader(http.StatusOK)
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
