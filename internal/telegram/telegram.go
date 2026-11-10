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
}

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
	if err := json.Unmarshal(raw, &r); err != nil || !r.OK {
		return fmt.Errorf("bot API answered %d: %s", resp.StatusCode, r.Description)
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
