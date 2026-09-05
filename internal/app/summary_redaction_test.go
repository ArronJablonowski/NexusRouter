package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/providers"
)

func TestRedactSummaryMessagesPreservesPairsPrecisionAndOriginal(t *testing.T) {
	secret := "quoted\"escaped\\line\nsecret"
	arguments, err := json.Marshal(map[string]any{
		secret: secret, "maximum": json.Number("18446744073709551615"),
		"nested": []any{secret, map[string]any{secret: "value " + secret}},
	})
	if err != nil {
		t.Fatal(err)
	}
	messages := []providers.Message{
		{Role: "user", Content: "request " + secret},
		{Role: "assistant", Content: "proposal " + secret, ToolCalls: []providers.ToolCall{{ID: "call-" + secret, Name: "tool-" + secret, Arguments: arguments}}},
		{Role: "tool", ToolCallID: "call-" + secret, Content: "result " + secret},
		{Role: "assistant", Content: "answer " + secret},
	}
	before, _ := json.Marshal(messages)
	got, err := redactSummaryMessages(messages, []string{secret})
	if err != nil {
		t.Fatal(err)
	}
	if err := providers.ValidateMessages(got); err != nil {
		t.Fatal("redaction broke call/result pairing", err)
	}
	for i, prefix := range []string{"request ", "proposal ", "result ", "answer "} {
		if got[i].Content != prefix+"[REDACTED]" {
			t.Fatal("content not redacted", i)
		}
	}
	call := got[1].ToolCalls[0]
	if call.ID != "call-[REDACTED]" || call.Name != "tool-[REDACTED]" || got[2].ToolCallID != call.ID {
		t.Fatal("tool identity not redacted consistently")
	}
	decoder := json.NewDecoder(bytes.NewReader(call.Arguments))
	decoder.UseNumber()
	var values map[string]any
	if err := decoder.Decode(&values); err != nil {
		t.Fatal("invalid redacted JSON", err)
	}
	if values["[REDACTED]"] != "[REDACTED]" || values["maximum"] != json.Number("18446744073709551615") {
		t.Fatal("key/value redaction or integer precision changed", values)
	}
	nested, ok := values["nested"].([]any)
	if !ok || len(nested) != 2 || nested[0] != "[REDACTED]" {
		t.Fatal("nested array not redacted", values)
	}
	if nested[1].(map[string]any)["[REDACTED]"] != "value [REDACTED]" {
		t.Fatal("nested key/value not redacted", nested)
	}
	if !bytes.Contains(call.Arguments, []byte("18446744073709551615")) {
		t.Fatal("uint64 maximum lost exact encoding")
	}
	after, _ := json.Marshal(messages)
	if !bytes.Equal(before, after) {
		t.Fatal("redaction mutated original messages")
	}
	got[0].Content = "changed"
	got[1].ToolCalls[0].Name = "changed"
	got[1].ToolCalls[0].Arguments[0] = '['
	after, _ = json.Marshal(messages)
	if !bytes.Equal(before, after) {
		t.Fatal("redacted output aliases original nested data")
	}
}

func TestRedactSummaryMessagesRejectsRedactedKeyCollisions(t *testing.T) {
	for _, arguments := range []string{
		`{"first-secret":1,"second-secret":2}`,
		`{"nested":[{"first-secret":1,"[REDACTED]":2}]}`,
	} {
		messages := []providers.Message{
			{Role: "assistant", ToolCalls: []providers.ToolCall{{ID: "call", Name: "lookup", Arguments: json.RawMessage(arguments)}}},
			{Role: "tool", ToolCallID: "call", Content: "result"},
		}
		before, _ := json.Marshal(messages)
		if got, err := redactSummaryMessages(messages, []string{"first-secret", "second-secret"}); !errors.Is(err, ErrAdmission) || got != nil {
			t.Fatal("colliding keys accepted", got, err)
		}
		after, _ := json.Marshal(messages)
		if !bytes.Equal(before, after) {
			t.Fatal("failed redaction mutated source")
		}
	}
}

func TestRedactSummaryMessagesRejectsRedactedDuplicateCallIDs(t *testing.T) {
	messages := []providers.Message{
		{Role: "assistant", ToolCalls: []providers.ToolCall{
			{ID: "first-secret", Name: "lookup", Arguments: json.RawMessage(`{}`)},
			{ID: "second-secret", Name: "lookup", Arguments: json.RawMessage(`{}`)},
		}},
		{Role: "tool", ToolCallID: "first-secret", Content: "first"},
		{Role: "tool", ToolCallID: "second-secret", Content: "second"},
	}
	if err := providers.ValidateMessages(messages); err != nil {
		t.Fatal("invalid fixture", err)
	}
	before, _ := json.Marshal(messages)
	if got, err := redactSummaryMessages(messages, []string{"first-secret", "second-secret"}); !errors.Is(err, ErrAdmission) || got != nil {
		t.Fatal("duplicate redacted call identities accepted", got, err)
	}
	after, _ := json.Marshal(messages)
	if !bytes.Equal(before, after) {
		t.Fatal("failed identity redaction mutated source")
	}
}
