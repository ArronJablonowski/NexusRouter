package webui

import (
	"strings"
	"time"
	"unicode/utf8"
)

// Model messages are untrusted knowledge, never instructions or routing evidence.
type CollaborationMessage struct {
	HarnessID   string    `json:"harness_id,omitempty"`
	SessionID   string    `json:"session_id"`
	Hostname    string    `json:"hostname"`
	Harness     string    `json:"harness"`
	Runner      string    `json:"runner"`
	Provider    string    `json:"provider"`
	Sequence    int64     `json:"sequence"`
	SentAt      time.Time `json:"sent_at"`
	Topic       string    `json:"topic"`
	SenderID    string    `json:"sender_id"`
	SenderModel string    `json:"sender_model"`
	Recipient   string    `json:"recipient"`
	TaskID      string    `json:"task_id"`
	Text        string    `json:"text"`
	Private     bool      `json:"private"`
}
type CollaborationOptions struct {
	Before int64
	Topic  string
}

func (o CollaborationOptions) Validate() error {
	if o.Before < 0 || !boundedPrintable(o.Topic, 0, 128) {
		return ErrContract
	}
	return nil
}

type CollaborationPage struct {
	Version    int                    `json:"version"`
	Enabled    bool                   `json:"enabled"`
	Messages   []CollaborationMessage `json:"messages"`
	NextBefore int64                  `json:"next_before"`
}

func (m CollaborationMessage) Validate() error {
	if !optionalModelID(m.HarnessID) || !optionalModelID(m.SessionID) || m.SessionID == "" || !boundedPrintable(m.Hostname, 1, 253) || !boundedPrintable(m.Harness, 1, 128) || !boundedPrintable(m.Runner, 1, 160) || !optionalModelID(m.Provider) || m.Provider == "" || m.Sequence < 1 || m.SentAt.IsZero() || !boundedPrintable(m.Topic, 1, 128) || !optionalModelID(m.SenderID) || m.SenderID == "" || !boundedPrintable(m.SenderModel, 1, 512) || !(m.Recipient == "*" || m.Recipient != "" && optionalModelID(m.Recipient)) || !optionalModelID(m.TaskID) || m.TaskID == "" || !utf8.ValidString(m.Text) || len(m.Text) > 8192 || strings.ContainsRune(m.Text, 0) || m.Text == "" {
		return ErrContract
	}
	return nil
}
func (p CollaborationPage) Validate() error {
	if p.Version != 1 || p.Messages == nil || len(p.Messages) > 50 || p.NextBefore < 0 {
		return ErrContract
	}
	var prev int64
	for _, m := range p.Messages {
		if m.Validate() != nil || prev != 0 && m.Sequence >= prev {
			return ErrContract
		}
		prev = m.Sequence
	}
	if p.NextBefore != 0 && (len(p.Messages) == 0 || p.NextBefore != prev) {
		return ErrContract
	}
	return encodedWithin(p, 512<<10)
}
