package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

const dynamicMutationSchema = `{"type":"object","properties":{"board_id":{"type":"string","pattern":"^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$"},"idempotency_key":{"type":"string","minLength":16,"maxLength":128}},"required":["board_id","idempotency_key"],"additionalProperties":false}`

func dynamicExecution(callID string, arguments json.RawMessage) runtime.ToolExecution {
	return runtime.ToolExecution{TaskID: "task", SessionID: "session", TurnID: "turn", AttemptID: "attempt",
		Call: providers.ToolCall{ID: callID, Name: "mutate_board", Arguments: arguments}}
}

func TestDynamicScopeRunsAfterSchemaAndBindsPolicyApprovalAndArguments(t *testing.T) {
	resolver, err := IdentifierScope("workboard", "board_id")
	if err != nil {
		t.Fatal(err)
	}
	registry := &Registry{}
	resolverCalls, handlerCalls, authorityCalls := 0, 0, 0
	definition := Definition{Tool: providers.Tool{Name: "mutate_board", Description: "Mutate one board",
		Parameters: json.RawMessage(dynamicMutationSchema)}, Scope: "workboard", Behavior: runtime.BehaviorIdempotentWrite,
		ResolveScope: func(raw json.RawMessage) (string, error) {
			resolverCalls++
			return resolver(raw)
		}, Handler: func(_ context.Context, raw json.RawMessage) (runtime.ToolResult, error) {
			handlerCalls++
			if string(raw) != `{"board_id":"board_a","idempotency_key":"operation-key-0001"}` {
				t.Fatalf("handler arguments changed: %s", raw)
			}
			return runtime.ToolResult{Content: `{"outcome":"committed"}`, Effect: runtime.ConfirmedEffect}, nil
		}}
	if err = registry.Register(definition); err != nil {
		t.Fatal(err)
	}
	arguments := json.RawMessage(`{"board_id":"board_a","idempotency_key":"operation-key-0001"}`)
	policy := &Policy{Default: Deny, Rules: []Rule{
		{Tool: "mutate_board", Scope: "workboard:forbidden", Decision: Deny},
		{Tool: "mutate_board", Scope: "workboard:board_a", Decision: Ask},
	}}
	authority := authorityFunc(func(ctx context.Context, authorization Authorization, invoke func(context.Context) (runtime.ToolResult, error)) (runtime.ToolResult, error) {
		authorityCalls++
		digest := sha256.Sum256(arguments)
		if authorization.Scope != "workboard:board_a" || authorization.ToolBehavior != runtime.BehaviorIdempotentWrite ||
			authorization.ArgumentsDigest != hex.EncodeToString(digest[:]) || string(authorization.Arguments) != string(arguments) {
			t.Fatalf("incorrect dynamic authorization: %+v", authorization)
		}
		return invoke(ctx)
	})
	executor := Executor{Registry: registry, Policy: policy, Authority: authority}
	out, err := executor.ExecuteScoped(context.Background(), dynamicExecution("call-one", arguments))
	if err != nil || out.Effect != runtime.ConfirmedEffect || out.Content != `{"outcome":"committed"}` || resolverCalls != 1 || authorityCalls != 1 || handlerCalls != 1 {
		t.Fatalf("out=%+v err=%v resolver=%d authority=%d handler=%d", out, err, resolverCalls, authorityCalls, handlerCalls)
	}
	// Missing and duplicate idempotency keys are rejected before scope,
	// approval, lease authority, or handler execution.
	for _, raw := range []string{`{"board_id":"board_a"}`, `{"board_id":"board_a","idempotency_key":"operation-key-0001","idempotency_key":"operation-key-0002"}`} {
		if _, err = executor.ExecuteScoped(context.Background(), dynamicExecution("invalid-call", json.RawMessage(raw))); !errors.Is(err, ErrArguments) {
			t.Fatalf("invalid idempotency arguments accepted: %v", err)
		}
	}
	if resolverCalls != 1 || authorityCalls != 1 || handlerCalls != 1 {
		t.Fatalf("invalid arguments crossed boundary: resolver=%d authority=%d handler=%d", resolverCalls, authorityCalls, handlerCalls)
	}
	denied := []byte(`{"board_id":"forbidden","idempotency_key":"operation-key-0002"}`)
	if _, err = executor.ExecuteScoped(context.Background(), dynamicExecution("denied-call", denied)); !errors.Is(err, ErrDenied) {
		t.Fatalf("exact dynamic deny was not enforced: %v", err)
	}
	if resolverCalls != 2 || authorityCalls != 1 || handlerCalls != 1 {
		t.Fatalf("denied scope crossed authority: resolver=%d authority=%d handler=%d", resolverCalls, authorityCalls, handlerCalls)
	}
}

func TestDynamicScopeIsNamespaceConfinedPanicSafeAndArgumentIsolated(t *testing.T) {
	for _, test := range []struct {
		name     string
		resolver func(json.RawMessage) (string, error)
	}{
		{"parent escape", func(json.RawMessage) (string, error) { return "workspace", nil }},
		{"sibling escape", func(json.RawMessage) (string, error) { return "workboards:board_a", nil }},
		{"wildcard", func(json.RawMessage) (string, error) { return "workboard:*", nil }},
		{"panic", func(json.RawMessage) (string, error) { panic("private resolver failure") }},
		{"error", func(json.RawMessage) (string, error) { return "", errors.New("private resolver failure") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry := &Registry{}
			handled, authorized := false, false
			if err := registry.Register(Definition{Tool: providers.Tool{Name: "mutate_board", Parameters: json.RawMessage(dynamicMutationSchema)},
				Scope: "workboard", ResolveScope: test.resolver, Behavior: runtime.BehaviorIdempotentWrite,
				Handler: func(context.Context, json.RawMessage) (runtime.ToolResult, error) {
					handled = true
					return runtime.ToolResult{Effect: runtime.ConfirmedEffect}, nil
				}}); err != nil {
				t.Fatal(err)
			}
			executor := Executor{Registry: registry, Policy: &Policy{Default: Ask}, Authority: authorityFunc(func(context.Context, Authorization, func(context.Context) (runtime.ToolResult, error)) (runtime.ToolResult, error) {
				authorized = true
				return runtime.ToolResult{Effect: runtime.ConfirmedEffect}, nil
			})}
			out, err := executor.ExecuteScoped(context.Background(), dynamicExecution("call", json.RawMessage(`{"board_id":"board_a","idempotency_key":"operation-key-0001"}`)))
			if !errors.Is(err, ErrDenied) || out.Effect != runtime.NoEffect || handled || authorized {
				t.Fatalf("scope escape crossed boundary: out=%+v err=%v handled=%v authorized=%v", out, err, handled, authorized)
			}
		})
	}

	registry := &Registry{}
	raw := json.RawMessage(`{"board_id":"board_a","idempotency_key":"operation-key-0001"}`)
	if err := registry.Register(Definition{Tool: providers.Tool{Name: "mutate_board", Parameters: json.RawMessage(dynamicMutationSchema)},
		Scope: "workboard", ResolveScope: func(copy json.RawMessage) (string, error) {
			copy[2] = 'X'
			return "workboard:board_a", nil
		}, Behavior: runtime.BehaviorIdempotentWrite, Handler: func(_ context.Context, got json.RawMessage) (runtime.ToolResult, error) {
			if string(got) != string(raw) {
				t.Fatalf("resolver rewrote handler arguments: %s", got)
			}
			return runtime.ToolResult{Effect: runtime.ConfirmedEffect}, nil
		}}); err != nil {
		t.Fatal(err)
	}
	executor := Executor{Registry: registry, Policy: &Policy{Default: Ask}, Authority: authorityFunc(func(ctx context.Context, _ Authorization, invoke func(context.Context) (runtime.ToolResult, error)) (runtime.ToolResult, error) {
		return invoke(ctx)
	})}
	if out, err := executor.ExecuteScoped(context.Background(), dynamicExecution("isolated-call", raw)); err != nil || out.Effect != runtime.ConfirmedEffect {
		t.Fatalf("isolated resolver failed: out=%+v err=%v", out, err)
	}
}

func TestDynamicMutationEffectClassificationNeverAuthorizesAutomaticReplay(t *testing.T) {
	resolver, err := IdentifierScope("workboard", "board_id")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		out  runtime.ToolResult
		err  error
		want runtime.ToolResult
		fail bool
	}{
		{"committed", runtime.ToolResult{Content: `{"outcome":"committed"}`, Effect: runtime.ConfirmedEffect}, nil, runtime.ToolResult{Content: `{"outcome":"committed"}`, Effect: runtime.ConfirmedEffect}, false},
		{"known conflict", runtime.ToolResult{Content: `{"error":"conflict"}`, Effect: runtime.NoEffect, Failed: true, Recoverable: true}, nil, runtime.ToolResult{Content: `{"error":"conflict"}`, Effect: runtime.NoEffect, Failed: true, Recoverable: true}, false},
		{"ambiguous", runtime.ToolResult{Content: "private", Effect: runtime.UncertainEffect}, errors.New("private storage acknowledgement"), runtime.ToolResult{Effect: runtime.UncertainEffect}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry := &Registry{}
			calls := 0
			if err := registry.Register(Definition{Tool: providers.Tool{Name: "mutate_board", Parameters: json.RawMessage(dynamicMutationSchema)},
				Scope: "workboard", ResolveScope: resolver, Behavior: runtime.BehaviorIdempotentWrite,
				Handler: func(context.Context, json.RawMessage) (runtime.ToolResult, error) { calls++; return test.out, test.err }}); err != nil {
				t.Fatal(err)
			}
			executor := Executor{Registry: registry, Policy: &Policy{Default: Ask}, Authority: authorityFunc(func(ctx context.Context, _ Authorization, invoke func(context.Context) (runtime.ToolResult, error)) (runtime.ToolResult, error) {
				return invoke(ctx)
			})}
			got, executionErr := executor.ExecuteScoped(context.Background(), dynamicExecution("effect-call", json.RawMessage(`{"board_id":"board_a","idempotency_key":"operation-key-0001"}`)))
			if test.fail != errors.Is(executionErr, ErrExecution) || got != test.want || calls != 1 {
				t.Fatalf("got=%+v err=%v calls=%d", got, executionErr, calls)
			}
		})
	}
}

func TestExtensionsCannotInstallDynamicScopeResolvers(t *testing.T) {
	resolver, err := IdentifierScope("workboard", "board_id")
	if err != nil {
		t.Fatal(err)
	}
	definition := Definition{Tool: providers.Tool{Name: "custom_mutation", Parameters: json.RawMessage(dynamicMutationSchema)},
		Scope: "workboard", ResolveScope: resolver, Behavior: runtime.BehaviorIdempotentWrite,
		Handler: func(context.Context, json.RawMessage) (runtime.ToolResult, error) {
			return runtime.ToolResult{Effect: runtime.ConfirmedEffect}, nil
		}}
	if extension, err := NewApprovalExtension([]Definition{definition}, &Policy{Default: Ask}); !errors.Is(err, ErrDefinition) || extension != nil {
		t.Fatalf("extension installed trusted scope resolver: extension=%+v err=%v", extension, err)
	}
}
