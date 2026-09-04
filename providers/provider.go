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
// Tool calls are proposals, never executed by a provider adapter.
type Provider interface {
	Stream(context.Context, Request, func(Chunk) error) error
	Models(context.Context) ([]string, error)
}
type Failure struct {
	Code      string
	Retryable bool
	Partial   bool
}

func (e *Failure) Error() string { return "provider: " + e.Code }
