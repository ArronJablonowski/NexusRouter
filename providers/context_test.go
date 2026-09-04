package providers

import (
	"encoding/json"
	"testing"
)

func TestContextEstimateIncludesSchemas(t *testing.T) {
	r := Request{Messages: []Message{{Role: "user", Content: "hello"}}}
	base, err := EstimateContext(r)
	if err != nil || base <= 1024 {
		t.Fatal(base, err)
	}
	r.Tools = []Tool{{Name: "read", Parameters: json.RawMessage(`{"type":"object"}`)}}
	withTool, err := EstimateContext(r)
	if err != nil || withTool <= base {
		t.Fatal(withTool, err)
	}
	r.JSONSchema = json.RawMessage(`{"type":"string"}`)
	withSchema, err := EstimateContext(r)
	if err != nil || withSchema <= withTool {
		t.Fatal(withSchema, err)
	}
	r.JSONSchema = json.RawMessage(`invalid`)
	if _, err := EstimateContext(r); err == nil {
		t.Fatal("invalid schema accepted")
	}
}
