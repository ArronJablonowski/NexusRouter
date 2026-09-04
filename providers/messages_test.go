package providers

import (
	"encoding/json"
	"testing"
)

func TestMessagePairing(t *testing.T) {
	call := ToolCall{ID: "a", Name: "lookup", Arguments: json.RawMessage(`{}`)}
	valid := []Message{{Role: "user", Content: "question"}, {Role: "assistant", ToolCalls: []ToolCall{call}}, {Role: "tool", ToolCallID: "a", Content: "result"}}
	if err := ValidateMessages(valid); err != nil {
		t.Fatal(err)
	}
	for _, messages := range [][]Message{nil, {{Role: "tool", ToolCallID: "orphan"}}, valid[:2], {valid[0], valid[1], {Role: "user", Content: "skip pending"}}, {valid[0], {Role: "assistant", ToolCalls: []ToolCall{call, call}}}, {{Role: "user", ToolCalls: []ToolCall{call}}}, {{Role: "user", ToolCallID: "a"}}} {
		if ValidateMessages(messages) == nil {
			t.Fatal("invalid pairing accepted")
		}
	}
}
