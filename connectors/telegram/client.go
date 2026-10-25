// Package telegram implements the bounded Telegram Bot API transport. It never
// discovers accounts or changes webhook configuration.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

var ErrAPI = errors.New("Telegram API unavailable")
var tokenPattern = regexp.MustCompile(`^[1-9][0-9]{0,19}:[A-Za-z0-9_-]{30,128}$`)

func BotID(token string) (string, error) {
	if !tokenPattern.MatchString(token) {
		return "", ErrAPI
	}
	return strings.SplitN(token, ":", 2)[0], nil
}

type User struct {
	ID  int64 `json:"id"`
	Bot bool  `json:"is_bot"`
}
type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}
type Message struct {
	ID   int64  `json:"message_id"`
	From *User  `json:"from"`
	Chat Chat   `json:"chat"`
	Text string `json:"text"`
}
type Update struct {
	ID      int64    `json:"update_id"`
	Message *Message `json:"message"`
}

type Client struct {
	endpoint string
	http     *http.Client
}

// NewClient performs no network I/O. Production endpoints cannot be overridden.
func NewClient(token string) (*Client, error) {
	if _, err := BotID(token); err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &Client{endpoint: "https://api.telegram.org/bot" + token + "/", http: &http.Client{
		Transport: transport, Timeout: 40 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

// call deliberately discards transport errors and remote descriptions: both may
// contain the credential-bearing request URL or private message content.
func (c *Client) call(ctx context.Context, method string, payload any, result any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return ErrAPI
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+method, bytes.NewReader(body))
	if err != nil {
		return ErrAPI
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return ErrAPI
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 || resp.StatusCode != http.StatusOK {
		return ErrAPI
	}
	var envelope struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal(data, &envelope) != nil || !envelope.OK || json.Unmarshal(envelope.Result, result) != nil {
		return ErrAPI
	}
	return nil
}

func (c *Client) Updates(ctx context.Context, offset int64) ([]Update, error) {
	if offset < 0 {
		return nil, ErrAPI
	}
	var updates []Update
	err := c.call(ctx, "getUpdates", struct {
		Offset  int64    `json:"offset"`
		Limit   int      `json:"limit"`
		Timeout int      `json:"timeout"`
		Allowed []string `json:"allowed_updates"`
	}{offset, 1, 25, []string{"message"}}, &updates)
	if len(updates) > 1 {
		return nil, ErrAPI
	}
	return updates, err
}

func (c *Client) Send(ctx context.Context, chat int64, text string) error {
	if chat <= 0 || !utf8.ValidString(text) || len(text) > 16384 || len(utf16.Encode([]rune(text))) > 4096 || strings.TrimSpace(text) == "" {
		return ErrAPI
	}
	var message Message
	err := c.call(ctx, "sendMessage", struct {
		Chat    int64  `json:"chat_id"`
		Text    string `json:"text"`
		Protect bool   `json:"protect_content"`
		Preview struct {
			Disabled bool `json:"is_disabled"`
		} `json:"link_preview_options"`
	}{Chat: chat, Text: text, Protect: true, Preview: struct {
		Disabled bool `json:"is_disabled"`
	}{true}}, &message)
	if err != nil || message.ID <= 0 || message.Chat.ID != chat || message.Chat.Type != "private" {
		return ErrAPI
	}
	return nil
}

// Close releases idle polling connections after shutdown.
func (c *Client) Close() { c.http.CloseIdleConnections() }
