package webui

import "encoding/json"

const MaxHistoryMessages = 100

type HistoryOptions struct {
	After string
	Limit int
}

func (o HistoryOptions) Validate() error {
	if o.Limit < 1 || o.Limit > MaxHistoryMessages || len(o.After) > MaxCursorBytes || (o.After != "" && !boundedPrintable(o.After, 1, MaxCursorBytes)) {
		return ErrContract
	}
	return nil
}

// HistoryMessage is an allowlisted browser presentation record. It cannot
// represent system prompts, tool calls, tool results, or provider metadata.
type HistoryMessage struct {
	ID             string `json:"id"`
	Role           string `json:"role"`
	Text           string `json:"text"`
	Revision       int64  `json:"revision"`
	SourceRevision int64  `json:"source_revision"`
}

func (m HistoryMessage) Validate() error {
	if !validID(m.ID) || (m.Role != "user" && m.Role != "assistant") || m.Revision < 1 || m.SourceRevision < 1 {
		return ErrContract
	}
	return requireText(m.Text, MaxEventBytes)
}

type HistoryPage struct {
	Version      int              `json:"version"`
	ChatID       string           `json:"chat_id"`
	TaskID       string           `json:"task_id"`
	HeadRevision int64            `json:"head_revision"`
	Messages     []HistoryMessage `json:"messages"`
	NextCursor   string           `json:"next_cursor"`
	HasMore      bool             `json:"has_more"`
}

func (p HistoryPage) Validate() error {
	if p.Version != ContractVersion || !validID(p.ChatID) || !validID(p.TaskID) || p.HeadRevision < 1 || len(p.Messages) > MaxHistoryMessages || p.HasMore != (p.NextCursor != "") || (p.HasMore && len(p.Messages) == 0) || (p.NextCursor != "" && !boundedPrintable(p.NextCursor, 1, MaxCursorBytes)) {
		return ErrContract
	}
	previous := int64(0)
	seen := map[string]bool{}
	for _, message := range p.Messages {
		if message.Validate() != nil || message.Revision <= previous || message.SourceRevision > p.HeadRevision || seen[message.ID] {
			return ErrContract
		}
		previous, seen[message.ID] = message.Revision, true
	}
	body, err := json.Marshal(p)
	if err != nil || len(body) > MaxEventBytes {
		return ErrContract
	}
	return nil
}

func ValidID(value string) bool { return validID(value) }
