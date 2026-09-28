// Package providers defines provider-neutral inference contracts.
package providers

import (
	"context"
	"encoding/json"
)

type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolFailed bool       `json:"tool_failed,omitempty"`
}
type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}
type Tool struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}
type Request struct {
	Model      string
	Messages   []Message
	Tools      []Tool
	JSONSchema json.RawMessage
	// MaxOutputTokens is a provider-enforced generation ceiling. Zero leaves
	// the provider default in effect.
	MaxOutputTokens int64
	// ContextTokens is the context allocation selected by the application for
	// this attempt, at or below the model's advertised capability ceiling.
	// Zero leaves the provider default in effect.
	ContextTokens int64
}

// MaxOutputTokens is the largest generation ceiling accepted by the provider
// boundary.
const MaxOutputTokens int64 = 1_000_000_000

func validMaxOutputTokens(limit int64) bool {
	return limit >= 0 && limit <= MaxOutputTokens
}

type Usage struct {
	InputTokens  int64
	OutputTokens int64
}
type Chunk struct {
	Text         string
	ToolCall     *ToolCall
	Usage        *Usage
	Done         bool
	FinishReason string
}

// Stream invokes emit sequentially with backpressure. Returning nil requires a
// verified completion marker; an interrupted stream returns a typed failure.
// Usage, when known, is emitted once as the total for this invocation. Adapters
// must consolidate incremental provider measurements before emitting Usage.
// Tool calls are proposals, never executed by a provider adapter.
type Provider interface {
	Stream(context.Context, Request, func(Chunk) error) error
	Models(context.Context) ([]string, error)
}

// ContextRolloverProvider is an optional lifecycle contract for providers that
// retain conversation state outside Request. CheckContextRollover must be
// read-only: current is the exact request completed by the provider and
// prospectiveReplacement is the request that would be dispatched after
// compaction, including prospective steering. ActivateContextRollover runs only
// after the replacement commits durably; activatedBase excludes any
// prospective steering that has not yet committed.
type ContextRolloverProvider interface {
	CheckContextRollover(context.Context, Request, Request) error
	ActivateContextRollover(context.Context, Request) error
}
type Failure struct {
	// StreamDetail is a bounded parser category, never upstream body text.
	StreamDetail string
	Code         string
	Retryable    bool
	Partial      bool
}

func (e *Failure) Error() string {
	detail := e.SafeStreamDetail()
	if detail != "" {
		return "provider: " + e.Code + " (" + detail + ")"
	}
	return "provider: " + e.Code
}
func (e *Failure) SafeStreamDetail() string {
	switch e.StreamDetail {
	case "byte_limit", "invalid_utf8", "invalid_json", "invalid_accounting_keys", "upstream_error", "invalid_tool_arguments", "invalid_usage_counts", "stream_read_error", "missing_done":
		return e.StreamDetail
	}
	return ""
}
