package app

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/providers"
)

func TestNativeSummaryMessagesStrictArgumentsAndEscapedResults(t *testing.T) {
	messages := []providers.Message{
		{Role: "user", Content: "request"},
		{Role: "assistant", ToolCalls: []providers.ToolCall{{ID: "call", Name: "fixture", Arguments: json.RawMessage(`{"value":"\u0073ecret","number":9007199254740993}`)}}},
		{Role: "tool", ToolCallID: "call", Content: `{"\u0073ecret":["\u0073ecret",9007199254740993]}`},
		{Role: "assistant", Content: "done"},
	}
	before, _ := json.Marshal(messages)
	clean, err := nativeSummaryMessages(messages, []string{"secret"})
	if err != nil || !strings.Contains(string(clean[1].ToolCalls[0].Arguments), "9007199254740993") || strings.Contains(string(clean[1].ToolCalls[0].Arguments), "secret") || strings.Contains(clean[2].Content, "secret") || !strings.Contains(clean[2].Content, "[REDACTED]") {
		t.Fatal("decoded source redaction failed", err)
	}
	after, _ := json.Marshal(messages)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("source changed")
	}
	for _, raw := range []string{`{"key":1,"key":2}`, `{"key":1,"\u006bey":2}`, `{"nested":{"key":1,"key":2}}`, `{} {}`, `[]`, `null`, `{"secret":1,"[REDACTED]":2}`, strings.Repeat(`{"a":`, 65) + `1` + strings.Repeat(`}`, 65)} {
		messages[1].ToolCalls[0].Arguments = json.RawMessage(raw)
		if _, err := nativeSummaryMessages(messages, []string{"secret"}); err == nil {
			t.Fatal("ambiguous or invalid arguments admitted")
		}
	}
}
