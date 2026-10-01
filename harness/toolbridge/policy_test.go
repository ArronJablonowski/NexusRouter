package toolbridge

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/tools"
)

// This exercises the real permission boundary, not durable runtime integration.
// Production Invoke must additionally own journal and execution leases.
func TestTokenAndRegistrationDoNotGrantToolPermission(t *testing.T) {
	for _, kind := range []string{"allowed_read", "policy_denied", "ask_without_authority", "write_without_authority", "schema_denied"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			registry := &tools.Registry{}
			schema := `{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}`
			if kind == "schema_denied" {
				schema = `{"type":"object","properties":{"path":{"const":"elsewhere"}},"required":["path"]}`
			}
			err := registry.Register(tools.Definition{Tool: providers.Tool{Name: "lookup", Description: "fixture", Parameters: json.RawMessage(schema)}, Scope: "fixture", ReadOnly: kind != "write_without_authority", Handler: func(ctx context.Context, _ json.RawMessage) (runtime.ToolResult, error) {
				identity, ok := tools.ExecutionIdentityFromContext(ctx)
				if !ok || identity.TaskID != "task" || identity.ToolCallID != "call" {
					t.Error("host execution identity lost")
				}
				calls++
				return runtime.ToolResult{Content: "permitted", Effect: runtime.NoEffect}, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			decision := tools.Allow
			if kind == "policy_denied" {
				decision = tools.Deny
			}
			if kind == "ask_without_authority" {
				decision = tools.Ask
			}
			executor := tools.Executor{Registry: registry, Policy: &tools.Policy{Default: decision}}
			b := bridge(t, executor.ExecuteScoped)
			w := request(b, `{"call_id":"call"}`)
			if kind == "allowed_read" {
				if w.Code != 200 || calls != 1 {
					t.Fatalf("read %d calls %d", w.Code, calls)
				}
			} else if w.Code != 409 || calls != 0 {
				t.Fatalf("permission bypass %d calls %d", w.Code, calls)
			}
		})
	}
}
