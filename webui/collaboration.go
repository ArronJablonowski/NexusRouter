package webui

import (
	"encoding/hex"
	"strings"
	"time"
	"unicode/utf8"
)

// Model messages are untrusted knowledge, never instructions or routing evidence.
type CollaborationMessage struct {
	SourceInstance string    `json:"source_instance,omitempty"`
	OriginInstance string    `json:"origin_instance,omitempty"`
	OriginStream   string    `json:"origin_stream,omitempty"`
	OriginSequence int64     `json:"origin_sequence,omitempty"`
	HarnessID      string    `json:"harness_id,omitempty"`
	SessionID      string    `json:"session_id"`
	Hostname       string    `json:"hostname"`
	Harness        string    `json:"harness"`
	Runner         string    `json:"runner"`
	Provider       string    `json:"provider"`
	Sequence       int64     `json:"sequence"`
	SentAt         time.Time `json:"sent_at"`
	Topic          string    `json:"topic"`
	SenderID       string    `json:"sender_id"`
	SenderModel    string    `json:"sender_model"`
	Recipient      string    `json:"recipient"`
	TaskID         string    `json:"task_id"`
	Text           string    `json:"text"`
	Private        bool      `json:"private"`
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

type CollaborationSync struct {
	Instance  string    `json:"instance"`
	State     string    `json:"state"`
	CheckedAt time.Time `json:"checked_at"`
}

func (m CollaborationMessage) ValidOrigin() bool {
	if !optionalModelID(m.OriginInstance) {
		return false
	}
	if m.OriginInstance != "" {
		raw, e := hex.DecodeString(m.OriginStream)
		return e == nil && len(raw) == 16
	}
	return true
}

type CollaborationPage struct {
	Sync       []CollaborationSync    `json:"sync,omitempty"`
	Version    int                    `json:"version"`
	Enabled    bool                   `json:"enabled"`
	Messages   []CollaborationMessage `json:"messages"`
	NextBefore int64                  `json:"next_before"`
}

func (m CollaborationMessage) Validate() error {
	if !optionalModelID(m.SourceInstance) || !m.ValidOrigin() || (m.OriginInstance == "" && (m.OriginStream != "" || m.OriginSequence != 0)) || (m.OriginInstance != "" && (len(m.OriginStream) != 32 || m.OriginSequence < 1)) || !optionalModelID(m.HarnessID) || !optionalModelID(m.SessionID) || m.SessionID == "" || !boundedPrintable(m.Hostname, 1, 253) || !boundedPrintable(m.Harness, 1, 128) || !boundedPrintable(m.Runner, 1, 160) || !optionalModelID(m.Provider) || m.Provider == "" || m.Sequence < 1 || m.SentAt.IsZero() || !boundedPrintable(m.Topic, 1, 128) || !optionalModelID(m.SenderID) || m.SenderID == "" || !boundedPrintable(m.SenderModel, 1, 512) || !(m.Recipient == "*" || m.Recipient != "" && optionalModelID(m.Recipient)) || !optionalModelID(m.TaskID) || m.TaskID == "" || !utf8.ValidString(m.Text) || len(m.Text) > 8192 || strings.ContainsRune(m.Text, 0) || m.Text == "" {
		return ErrContract
	}
	return nil
}
func (p CollaborationPage) Validate() error {
	if p.Version != 1 || p.Messages == nil || len(p.Messages) > 50 || p.NextBefore < 0 {
		return ErrContract
	}
	if len(p.Sync) > 128 {
		return ErrContract
	}
	for _, s := range p.Sync {
		if !optionalModelID(s.Instance) || s.Instance == "" || (s.State != "synced" && s.State != "unavailable" && s.State != "catching_up") || s.CheckedAt.IsZero() {
			return ErrContract
		}
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
