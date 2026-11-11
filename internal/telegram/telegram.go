// Package telegram sends HTML messages to one chat through the Bot API.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// DefaultBaseURL is the Bot API's address.
const DefaultBaseURL = "https://api.telegram.org"

// Client posts messages to one chat as one bot.
type Client struct {
	// BaseURL is the Bot API's address; DefaultBaseURL when empty.
	BaseURL string
	Token   string
	ChatID  string
	HTTP    *http.Client
}

// New returns a client for the bot with this token, posting to chatID.
func New(token, chatID string) *Client {
	return &Client{BaseURL: DefaultBaseURL, Token: token, ChatID: chatID, HTTP: &http.Client{Timeout: 15 * time.Second}}
}

type sendMessage struct {
	ChatID                string `json:"chat_id"`
	Text                  string `json:"text"`
	ParseMode             string `json:"parse_mode"`
	DisableWebPagePreview bool   `json:"disable_web_page_preview"`
}

type response struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
	Parameters  *struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

// Error is the Bot API's refusal of a message.
type Error struct {
	Status      int
	Description string
	// After is how long the Bot API asked to wait (retry_after), when it did.
	After time.Duration
}

func (e *Error) Error() string {
	return fmt.Sprintf("bot API answered %d: %s", e.Status, e.Description)
}

// Temporary reports whether sending again may succeed: a rate limit or a
// server error. Any other refusal (a wrong chat, broken HTML) stays.
func (e *Error) Temporary() bool {
	return e.Status == http.StatusTooManyRequests || e.Status >= http.StatusInternalServerError
}

// RetryAfter is how long the Bot API asked to wait before the next message.
func (e *Error) RetryAfter() time.Duration { return e.After }

// Send posts text, written in Telegram's HTML subset, to the chat.
func (c *Client) Send(ctx context.Context, text string) error {
	body, err := json.Marshal(sendMessage{ChatID: c.ChatID, Text: text, ParseMode: "HTML", DisableWebPagePreview: true})
	if err != nil {
		return fmt.Errorf("encoding the message: %w", err)
	}

	base := c.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/bot"+c.Token+"/sendMessage", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building the request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		// The URL carries the token; never let it reach a log.
		return fmt.Errorf("calling the Bot API: %w", redact(err, c.Token))
	}
	defer func() { _ = resp.Body.Close() }()

	var r response

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err := json.Unmarshal(raw, &r); err != nil {
		// A proxy in the way answers HTML; keep the status so a 5xx is retried.
		return &Error{Status: resp.StatusCode, Description: "unreadable answer"}
	}

	if !r.OK {
		e := &Error{Status: resp.StatusCode, Description: r.Description}
		if r.Parameters != nil && r.Parameters.RetryAfter > 0 {
			e.After = time.Duration(r.Parameters.RetryAfter) * time.Second
		}

		return e
	}

	return nil
}

type redactedError struct{ msg string }

func (e redactedError) Error() string { return e.msg }

func redact(err error, token string) error {
	if token == "" {
		return err
	}

	return redactedError{msg: string(bytes.ReplaceAll([]byte(err.Error()), []byte(token), []byte("<token>")))}
}
