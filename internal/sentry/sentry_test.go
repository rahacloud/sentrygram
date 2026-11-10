package sentry_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/rahacloud/sentrygram/internal/relay"
	"github.com/rahacloud/sentrygram/internal/sentry"
)

func server(t *testing.T) *httptest.Server {
	t.Helper()

	body, err := os.ReadFile("testdata/latest-event.json")
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/0/organizations/acme/issues/283/events/latest/" {
			http.NotFound(w, r)

			return
		}

		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)

			return
		}

		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	return srv
}

func TestLatestEvent(t *testing.T) {
	t.Parallel()

	srv := server(t)
	issue := relay.Issue{ID: "283", WebURL: "https://sentry.example/organizations/acme/issues/283/"}

	ev, err := sentry.New(srv.URL, "tok", "").LatestEvent(t.Context(), issue)
	if err != nil {
		t.Fatal(err)
	}

	if ev.Environment != "production" || ev.Release != "1.4.2" || ev.ServerName != "web-1" || ev.Runtime != "CPython 3.14.0" {
		t.Errorf("tags: %+v", ev)
	}

	if ev.Handled == nil || *ev.Handled {
		t.Errorf("handled = %v, want the raised exception's false", ev.Handled)
	}

	f := ev.Frame
	if f == nil || f.File != "app/orders/views.py" || f.Line != 58 || f.Function != "retrieve" || f.Code != "    return Response(total(order))" {
		t.Errorf("frame = %+v, want the innermost in-app frame of the last exception", f)
	}

	if ev.Method != http.MethodGet || !strings.HasPrefix(ev.URL, "https://api.example.com/api/v1/orders/42/") {
		t.Errorf("request = %s %s", ev.Method, ev.URL)
	}

	msg := relay.Message(relay.Issue{ID: "283", ShortID: "ACME-7", Level: "error", Platform: "python", Count: "3", UserCount: 2,
		Project: relay.Project{Slug: "orders"}, Metadata: relay.Metadata{Type: "ValueError", Value: "order 42 has no total"}}, relay.Created, ev, "https://sentry.example")

	for _, want := range []string{
		"<b>orders</b> · production",
		"📍 <code>app/orders/views.py:58 in retrieve</code>",
		"<pre>return Response(total(order))</pre>",
		"🌐 GET https://api.example.com/api/v1/orders/42/\n",
		"release 1.4.2 · python · 🔥 unhandled · server web-1 · CPython 3.14.0",
		"📊 3 events · 2 users",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q:\n%s", want, msg)
		}
	}

	if strings.Contains(msg, "secret") {
		t.Errorf("message leaks the query string:\n%s", msg)
	}
}

func TestLatestEventErrors(t *testing.T) {
	t.Parallel()

	srv := server(t)

	if _, err := sentry.New(srv.URL, "wrong", "acme").LatestEvent(t.Context(), relay.Issue{ID: "283"}); err == nil {
		t.Error("a refused token gave no error")
	}

	if _, err := sentry.New(srv.URL, "tok", "").LatestEvent(t.Context(), relay.Issue{ID: "283"}); err == nil {
		t.Error("no organization gave no error")
	}
}
