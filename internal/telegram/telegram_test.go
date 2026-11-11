package telegram_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rahacloud/sentrygram/internal/telegram"
)

func TestSend(t *testing.T) {
	t.Parallel()

	var got map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/botTOKEN/sendMessage" {
			t.Errorf("path = %s", r.URL.Path)
		}

		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := telegram.New("TOKEN", "-42")
	c.BaseURL = srv.URL

	if err := c.Send(t.Context(), "<b>hi</b>"); err != nil {
		t.Fatal(err)
	}

	if got["chat_id"] != "-42" || got["text"] != "<b>hi</b>" || got["parse_mode"] != "HTML" {
		t.Fatalf("body = %v", got)
	}
}

func TestSendReportsRefusal(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"description":"Bad Request: chat not found"}`))
	}))
	defer srv.Close()

	c := telegram.New("TOKEN", "-42")
	c.BaseURL = srv.URL

	err := c.Send(t.Context(), "x")
	if err == nil || !strings.Contains(err.Error(), "chat not found") {
		t.Fatalf("err = %v", err)
	}
}

func TestSendNeverLogsTheToken(t *testing.T) {
	t.Parallel()

	c := telegram.New("SECRET123", "-42")
	c.BaseURL = "http://127.0.0.1:1"

	err := c.Send(t.Context(), "x")
	if err == nil || strings.Contains(err.Error(), "SECRET123") {
		t.Fatalf("err = %v", err)
	}
}

func TestSendReportsRateLimit(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"ok":false,"description":"Too Many Requests: retry after 7","parameters":{"retry_after":7}}`))
	}))
	defer srv.Close()

	c := telegram.New("TOKEN", "-42")
	c.BaseURL = srv.URL

	var e *telegram.Error
	if err := c.Send(t.Context(), "x"); !errors.As(err, &e) {
		t.Fatalf("err = %v, want a *telegram.Error", err)
	}

	if !e.Temporary() || e.RetryAfter() != 7*time.Second {
		t.Fatalf("temporary = %v, retry after = %s", e.Temporary(), e.RetryAfter())
	}
}

func TestRefusalIsPermanent(t *testing.T) {
	t.Parallel()

	if (&telegram.Error{Status: http.StatusBadRequest}).Temporary() {
		t.Fatal("a 400 must not be retried")
	}

	if !(&telegram.Error{Status: http.StatusBadGateway}).Temporary() {
		t.Fatal("a 502 must be retried")
	}
}
