package relay_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rahacloud/sentrygram/internal/relay"
)

// fakeSender records every attempt and fails the first len(errs) of them.
type fakeSender struct {
	mu   sync.Mutex
	sent []string
	errs []error
}

func (f *fakeSender) Send(_ context.Context, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.sent = append(f.sent, text)
	if n := len(f.sent); n <= len(f.errs) {
		return f.errs[n-1]
	}

	return nil
}

func (f *fakeSender) messages() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]string(nil), f.sent...)
}

type permanent struct{}

func (permanent) Error() string   { return "chat not found" }
func (permanent) Temporary() bool { return false }

type limited struct{}

func (limited) Error() string             { return "too many requests" }
func (limited) RetryAfter() time.Duration { return 20 * time.Millisecond }

const created = `{"action":"created","data":{"issue":{"id":"283","shortId":"GOOGLE-INTEGRATION-6","title":"boom",
"culprit":"apps.views in get","level":"error","web_url":"https://sentry.example/issues/283/",
"project":{"slug":"google-integration"},"metadata":{"type":"ValueError","value":"a < b"}}}}`

func newHandler(s *fakeSender) *relay.Handler {
	return &relay.Handler{
		Sender:        s,
		PathToken:     "secret-token",
		SentryURL:     "https://sentry.example",
		NoiseCulprits: []string{"django.core.management.commands.shell"},
		RetryDelay:    time.Millisecond,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func post(t *testing.T, h http.Handler, path, resource, body string, header map[string]string) int {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Sentry-Hook-Resource", resource)

	for k, v := range header {
		req.Header.Set(k, v)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	return rec.Code
}

// drain waits for the background deliveries, as the server does on shutdown.
func drain(t *testing.T, h *relay.Handler) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	if err := h.Drain(ctx); err != nil {
		t.Fatalf("drain: %v", err)
	}
}

func TestPostsCreatedIssue(t *testing.T) {
	t.Parallel()

	s := &fakeSender{}
	h := newHandler(s)

	if code := post(t, h, "/hooks/telegram/secret-token", "issue", created, nil); code != http.StatusAccepted {
		t.Fatalf("code = %d", code)
	}

	drain(t, h)

	sent := s.messages()
	if len(sent) != 1 {
		t.Fatalf("sent %d messages", len(sent))
	}

	for _, want := range []string{"🚨 <b>ERROR</b> · <b>google-integration</b>", "ValueError: a &lt; b", "apps.views in get", `href="https://sentry.example/issues/283/"`, "GOOGLE-INTEGRATION-6 →"} {
		if !strings.Contains(sent[0], want) {
			t.Errorf("message lacks %q:\n%s", want, sent[0])
		}
	}
}

func TestRefusesWrongPath(t *testing.T) {
	t.Parallel()

	s := &fakeSender{}
	h := newHandler(s)

	if code := post(t, h, "/hooks/telegram/nope", "issue", created, nil); code != http.StatusNotFound {
		t.Fatalf("code = %d", code)
	}

	drain(t, h)

	if len(s.messages()) != 0 {
		t.Fatal("posted without the token")
	}
}

func TestChecksSignature(t *testing.T) {
	t.Parallel()

	s := &fakeSender{}
	h := newHandler(s)
	h.ClientSecret = "shh"

	if code := post(t, h, "/x/secret-token", "issue", created, map[string]string{"Sentry-Hook-Signature": "bad"}); code != http.StatusNotFound {
		t.Fatalf("bad signature: code = %d", code)
	}

	mac := hmac.New(sha256.New, []byte("shh"))
	mac.Write([]byte(created))

	sig := hex.EncodeToString(mac.Sum(nil))
	if code := post(t, h, "/x/secret-token", "issue", created, map[string]string{"Sentry-Hook-Signature": sig}); code != http.StatusAccepted {
		t.Fatalf("good signature: code = %d", code)
	}

	drain(t, h)

	if n := len(s.messages()); n != 1 {
		t.Fatalf("sent %d messages", n)
	}
}

func TestDropsOtherEventsAndNoise(t *testing.T) {
	t.Parallel()

	s := &fakeSender{}
	h := newHandler(s)
	path := "/hooks/telegram/secret-token"

	resolved := strings.Replace(created, `"created"`, `"resolved"`, 1)
	noise := strings.Replace(created, "apps.views in get", "django.core.management.commands.shell in handle", 1)

	for name, c := range map[string][2]string{
		"error resource": {"error", created},
		"resolved":       {"issue", resolved},
		"qa shell":       {"issue", noise},
	} {
		if code := post(t, h, path, c[0], c[1], nil); code != http.StatusOK {
			t.Errorf("%s: code = %d", name, code)
		}
	}

	drain(t, h)

	if n := len(s.messages()); n != 0 {
		t.Fatalf("posted %d messages, want none", n)
	}
}

func TestReturningIssue(t *testing.T) {
	t.Parallel()

	s := &fakeSender{}
	h := newHandler(s)
	back := strings.Replace(created, `"created"`, `"unresolved"`, 1)

	if code := post(t, h, "/hooks/telegram/secret-token", "issue", back, nil); code != http.StatusAccepted {
		t.Fatalf("code = %d", code)
	}

	drain(t, h)

	if sent := s.messages(); len(sent) != 1 || !strings.Contains(sent[0], "🔁") || !strings.Contains(sent[0], "back again") {
		t.Fatalf("message: %v", sent)
	}
}

func TestAnswersBeforeTelegram(t *testing.T) {
	t.Parallel()

	// Sentry gives a webhook one second; a slow Telegram must not hold the answer.
	release := make(chan struct{})
	s := &blockingSender{release: release}
	h := newHandler(&fakeSender{})
	h.Sender = s

	start := time.Now()
	if code := post(t, h, "/hooks/telegram/secret-token", "issue", created, nil); code != http.StatusAccepted {
		t.Fatalf("code = %d", code)
	}

	if took := time.Since(start); took > 500*time.Millisecond {
		t.Fatalf("answered after %s", took)
	}

	close(release)
	drain(t, h)
}

type blockingSender struct{ release chan struct{} }

func (b *blockingSender) Send(ctx context.Context, _ string) error {
	select {
	case <-b.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestRetries(t *testing.T) {
	t.Parallel()

	for name, c := range map[string]struct {
		errs     []error
		attempts int
	}{
		"recovers":           {errs: []error{errors.New("down"), limited{}}, attempts: 3},
		"gives up":           {errs: []error{errors.New("down"), errors.New("down"), errors.New("down"), errors.New("down"), errors.New("down")}, attempts: 4},
		"permanent refusals": {errs: []error{permanent{}}, attempts: 1},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s := &fakeSender{errs: c.errs}
			h := newHandler(s)

			if code := post(t, h, "/hooks/telegram/secret-token", "issue", created, nil); code != http.StatusAccepted {
				t.Fatalf("code = %d", code)
			}

			drain(t, h)

			if n := len(s.messages()); n != c.attempts {
				t.Fatalf("attempts = %d, want %d", n, c.attempts)
			}
		})
	}
}

func TestLinkFallsBackToSentryURL(t *testing.T) {
	t.Parallel()

	msg := relay.Message(relay.Issue{ID: "7", Title: "x"}, relay.Created, nil, "https://sentry.example/")
	if !strings.Contains(msg, `href="https://sentry.example/issues/7/"`) || !strings.Contains(msg, "Open in Sentry →") {
		t.Fatalf("message: %s", msg)
	}
}

type fakeEvents struct {
	ev  *relay.Event
	err error
}

func (f fakeEvents) LatestEvent(context.Context, relay.Issue) (*relay.Event, error) {
	return f.ev, f.err
}

func TestEventDetailsAndTheirAbsence(t *testing.T) {
	t.Parallel()

	s := &fakeSender{}
	h := newHandler(s)
	h.Events = fakeEvents{ev: &relay.Event{Environment: "staging"}}

	post(t, h, "/hooks/telegram/secret-token", "issue", created, nil)
	drain(t, h)

	h.Events = fakeEvents{err: errors.New("sentry down")}
	if code := post(t, h, "/hooks/telegram/secret-token", "issue", created, nil); code != http.StatusAccepted {
		t.Fatalf("a failed event read must not stop the message: code = %d", code)
	}

	drain(t, h)

	if sent := s.messages(); len(sent) != 2 || !strings.Contains(sent[0], "· staging") || strings.Contains(sent[1], "staging") {
		t.Fatalf("messages: %q", sent)
	}
}

func TestHandledFlag(t *testing.T) {
	t.Parallel()

	yes, no := true, false
	unhandled := relay.Issue{ID: "1", Title: "x", IsUnhandled: true}

	for name, c := range map[string]struct {
		issue relay.Issue
		event *relay.Event
		want  string
	}{
		"no event":               {unhandled, nil, "🔥 unhandled"},
		"event without the flag": {unhandled, &relay.Event{}, "🔥 unhandled"},
		"event says unhandled":   {relay.Issue{ID: "1"}, &relay.Event{Handled: &no}, "🔥 unhandled"},
		"event says handled":     {unhandled, &relay.Event{Handled: &yes}, "handled"},
	} {
		msg := relay.Message(c.issue, relay.Created, c.event, "https://sentry.example")
		if !strings.Contains(msg, c.want) || (c.want == "handled" && strings.Contains(msg, "unhandled")) {
			t.Errorf("%s: message lacks %q:\n%s", name, c.want, msg)
		}
	}
}
