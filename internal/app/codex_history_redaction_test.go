package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/providers"
)

func codexRedactionMessages(content string) []providers.Message {
	return []providers.Message{
		{Role: "user", Content: "request secret"},
		{Role: "assistant", ToolCalls: []providers.ToolCall{{ID: "call", Name: "delegate", Arguments: json.RawMessage(`{"text":"secret"}`)}}},
		{Role: "tool", ToolCallID: "call", Content: content},
		{Role: "user", Content: "continue"},
	}
}

func TestRedactCodexHistoryStructuredEscapes(t *testing.T) {
	secret := "quoted\"back\\line\n秘密"
	body, _ := json.Marshal(map[string]any{secret: secret, "number": json.Number("18446744073709551615"), "array": []any{secret}})
	messages := codexRedactionMessages(string(body))
	before, _ := json.Marshal(messages)
	clean, err := redactCodexHistoryMessages(messages, []string{secret, "secret"})
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	decoder := json.NewDecoder(strings.NewReader(clean[2].Content))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil || value["[REDACTED]"] != "[REDACTED]" || value["number"] != json.Number("18446744073709551615") || value["array"].([]any)[0] != "[REDACTED]" {
		t.Fatal("structured redaction lost data or leaked decoded secret")
	}
	if clean[0].Content != "request [REDACTED]" || string(clean[1].ToolCalls[0].Arguments) != `{"text":"[REDACTED]"}` {
		t.Fatal("ordinary redaction changed")
	}
	after, _ := json.Marshal(messages)
	if !bytes.Equal(before, after) {
		t.Fatal("source mutated")
	}
	clean, err = redactCodexHistoryMessages(codexRedactionMessages(` ["\u0073ecret",{"\u0073ecret":"\u0073ecret"}] `), []string{"secret"})
	if err != nil || clean[2].Content != `["[REDACTED]",{"[REDACTED]":"[REDACTED]"}]` {
		t.Fatal("Unicode escape or array redaction failed")
	}
}

func TestRedactCodexHistoryRejectsStructuredAmbiguity(t *testing.T) {
	for name, content := range map[string]string{
		"duplicate":         `{"x":1,"x":2}`,
		"escaped duplicate": `{"x":1,"\u0078":2}`,
		"collision":         `{"secret":1,"[REDACTED]":2}`,
		"malformed":         `{not json}`,
		"trailing":          `{} []`,
		"depth":             strings.Repeat("[", 65) + "0" + strings.Repeat("]", 65),
		"size":              `{"x":"` + strings.Repeat("x", 1<<20) + `"}`,
		"invalid UTF8":      "[\"" + string([]byte{255}) + "\"]",
	} {
		t.Run(name, func(t *testing.T) {
			clean, err := redactCodexHistoryMessages(codexRedactionMessages(content), []string{"secret"})
			if !errors.Is(err, ErrAdmission) || clean != nil {
				t.Fatal("ambiguous structured content admitted")
			}
		})
	}
}

func TestRedactCodexHistoryPlaintextAndDepthBoundary(t *testing.T) {
	for _, content := range []string{"plain secret", "", "not JSON { secret }"} {
		messages := codexRedactionMessages(content)
		want, _ := redactSummaryMessages(messages, []string{"secret"})
		got, err := redactCodexHistoryMessages(messages, []string{"secret"})
		if err != nil || got[2].Content != want[2].Content {
			t.Fatal("plaintext behavior changed")
		}
	}
	content := strings.Repeat("[", 64) + "0" + strings.Repeat("]", 64)
	got, err := redactCodexHistoryMessages(codexRedactionMessages(content), nil)
	if err != nil || got[2].Content != content {
		t.Fatal("depth 64 rejected")
	}
}
