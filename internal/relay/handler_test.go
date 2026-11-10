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
	"testing"

	"github.com/rahacloud/sentrygram/internal/relay"
)

type fakeSender struct {
	sent []string
	err  error
}

func (f *fakeSender) Send(_ context.Context, text string) error {
	f.sent = append(f.sent, text)

	return f.err
}

const created = `{"action":"created","data":{"issue":{"id":"283","shortId":"GOOGLE-INTEGRATION-6","title":"boom",
"culprit":"apps.views in get","level":"error","web_url":"https://sentry.example/issues/283/",
"project":{"slug":"google-integration"},"metadata":{"type":"ValueError","value":"a < b"}}}}`

func newHandler(s *fakeSender) *relay.Handler {
	return &relay.Handler{
		Sender:        s,
		PathToken:     "secret-token",
		SentryURL:     "https://sentry.example",
		NoiseCulprits: []string{"django.core.management.commands.shell"},
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

func TestPostsCreatedIssue(t *testing.T) {
	t.Parallel()

	s := &fakeSender{}
	if code := post(t, newHandler(s), "/hooks/telegram/secret-token", "issue", created, nil); code != http.StatusOK {
		t.Fatalf("code = %d", code)
	}

	if len(s.sent) != 1 {
		t.Fatalf("sent %d messages", len(s.sent))
	}

	for _, want := range []string{"🚨 <b>ERROR</b> · <b>google-integration</b>", "ValueError: a &lt; b", "apps.views in get", `href="https://sentry.example/issues/283/"`, "GOOGLE-INTEGRATION-6 →"} {
		if !strings.Contains(s.sent[0], want) {
			t.Errorf("message lacks %q:\n%s", want, s.sent[0])
		}
	}
}

func TestRefusesWrongPath(t *testing.T) {
	t.Parallel()

	s := &fakeSender{}
	if code := post(t, newHandler(s), "/hooks/telegram/nope", "issue", created, nil); code != http.StatusNotFound {
		t.Fatalf("code = %d", code)
	}

	if len(s.sent) != 0 {
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
	if code := post(t, h, "/x/secret-token", "issue", created, map[string]string{"Sentry-Hook-Signature": sig}); code != http.StatusOK {
		t.Fatalf("good signature: code = %d", code)
	}

	if len(s.sent) != 1 {
		t.Fatalf("sent %d messages", len(s.sent))
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

	if len(s.sent) != 0 {
		t.Fatalf("posted %d messages, want none", len(s.sent))
	}
}

func TestReturningIssueAndTelegramFailure(t *testing.T) {
	t.Parallel()

	s := &fakeSender{err: errors.New("down")}
	back := strings.Replace(created, `"created"`, `"unresolved"`, 1)

	if code := post(t, newHandler(s), "/hooks/telegram/secret-token", "issue", back, nil); code != http.StatusBadGateway {
		t.Fatalf("code = %d", code)
	}

	if len(s.sent) != 1 || !strings.Contains(s.sent[0], "🔁") || !strings.Contains(s.sent[0], "back again") {
		t.Fatalf("message: %v", s.sent)
	}
}

func TestLinkFallsBackToSentryURL(t *testing.T) {
	t.Parallel()

	msg := relay.Message(relay.Issue{ID: "7", Title: "x"}, relay.Created, "https://sentry.example/")
	if !strings.Contains(msg, `href="https://sentry.example/issues/7/"`) || !strings.Contains(msg, "Open in Sentry →") {
		t.Fatalf("message: %s", msg)
	}
}
