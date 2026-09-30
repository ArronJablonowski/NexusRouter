package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/contextengine"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

func TestContextEngineEscapedToolOutputRedaction(t *testing.T) {
	const original = `{"output":"s\u0065cret"}`
	input := []providers.Message{
		{Role: "assistant", ToolCalls: []providers.ToolCall{{ID: "call", Name: "read_file", Arguments: json.RawMessage(`{}`)}}},
		{Role: "tool", ToolCallID: "call", Content: original},
		{Role: "user", Content: "inspect result"},
	}
	calls := 0
	engine := applicationContextEngine{assembly: func(ctx context.Context, a contextengine.Assembly) (contextengine.Plan, error) {
		calls++
		var data map[string]string
		if json.Unmarshal([]byte(a.Current[1].Content), &data) != nil || data["output"] != "[REDACTED]" {
			t.Error("escaped credential reached engine")
		}
		return (contextengine.Default{}).Assemble(ctx, a)
	}}
	r := Request{Messages: input, contextEngine: engine}
	got, err := prepareTaskContext(context.Background(), &r, []string{"secret"})
	if err != nil || calls != 1 || len(got) != 3 || input[1].Content != original || strings.Contains(got[1].Content, `\u0065`) {
		t.Fatal("redaction changed caller or lost messages", err)
	}
}

func TestContextEnginePreflightBeforeRedactionOrCallback(t *testing.T) {
	called := false
	engine := applicationContextEngine{
		assembly: func(context.Context, contextengine.Assembly) (contextengine.Plan, error) {
			called = true
			return contextengine.Plan{}, nil
		},
		compact: func(context.Context, sessions.Snapshot, sessions.CompactionRequest) (sessions.CompactionRequest, error) {
			called = true
			return sessions.CompactionRequest{}, nil
		},
	}
	huge := []providers.Message{{Role: "user", Content: strings.Repeat("x", 4<<20)}, {Role: "assistant", Content: "answer"}}
	r := Request{Prompt: "next", contextEngine: engine, continuation: &continuationContext{Messages: huge}}
	if _, err := prepareTaskContext(context.Background(), &r, nil); err != ErrAdmission || called {
		t.Fatal("oversized assembly reached engine", err)
	}
	source := sessions.Snapshot{TaskID: "source", State: "completed", Sequence: 10, Messages: huge}
	req := sessions.CompactionRequest{Keep: 1, Summary: sessions.Summary{Decisions: []string{"retain"}}}
	if _, err := selectContextCompaction(context.Background(), engine, source, req, nil); err != ErrAdmission || called {
		t.Fatal("oversized compaction reached engine", err)
	}
}
