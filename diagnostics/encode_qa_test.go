package diagnostics_test

import (
	"encoding/json"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/diagnostics"
	"github.com/ArronJablonowski/DarwinRouter/providers"
)

func TestDiagnosticRedactionRejectsCollidingObjectKeys(t *testing.T) {
	record := diagnostics.Record{Content: &diagnostics.Content{
		ToolCalls: []providers.ToolCall{{ID: "call", Name: "tool", Arguments: json.RawMessage(`{"secret-token":"first","[REDACTED]":"second"}`)}},
	}}
	if body, err := diagnostics.EncodeLine(record, []string{"secret-token"}); err == nil || len(body) != 0 {
		t.Fatalf("redaction silently discarded a structured argument: %s, %v", body, err)
	}
}

func TestDiagnosticRedactionRejectsTwoSecretsBecomingTheSameKey(t *testing.T) {
	record := diagnostics.Record{Content: &diagnostics.Content{
		ToolCalls: []providers.ToolCall{{ID: "call", Name: "tool", Arguments: json.RawMessage(`{"prefix-first-secret":"first","prefix-second-secret":"second"}`)}},
	}}
	if body, err := diagnostics.EncodeLine(record, []string{"first-secret", "second-secret"}); err == nil || len(body) != 0 {
		t.Fatalf("redaction silently discarded a distinct secret-bearing argument: %s, %v", body, err)
	}
}
