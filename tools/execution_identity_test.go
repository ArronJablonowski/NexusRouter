package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func TestExecutionIdentityScopedReplacementAndUnscopedMask(t *testing.T) {
	var captured context.Context
	var got ExecutionIdentity
	var present bool
	counter := 0
	d := definition(&counter)
	d.Handler = func(ctx context.Context, _ json.RawMessage) (runtime.ToolResult, error) {
		captured = ctx
		got, present = ExecutionIdentityFromContext(ctx)
		return runtime.ToolResult{Effect: runtime.NoEffect}, nil
	}
	r := &Registry{}
	if err := r.Register(d); err != nil {
		t.Fatal(err)
	}
	e := Executor{Registry: r, Policy: &Policy{Default: Allow}}
	x := authorizedExecution()
	if _, err := e.ExecuteScoped(context.Background(), x); err != nil || !present || got.TaskID != x.TaskID || got.ToolCallID != x.Call.ID || got.ToolName != x.Call.Name {
		t.Fatal("scoped identity missing", got, err)
	}
	got.TaskID = "changed by getter caller"
	if again, ok := ExecutionIdentityFromContext(captured); !ok || again.TaskID != x.TaskID {
		t.Fatal("getter did not return value copy")
	}
	prior := captured
	if _, err := e.Execute(prior, x.Call); err != nil || present {
		t.Fatal("unscoped invocation inherited parent identity", err)
	}
	x.TaskID, x.Call.ID = "nested-task", "nested-call"
	if _, err := e.ExecuteScoped(prior, x); err != nil || !present || got.TaskID != "nested-task" || got.ToolCallID != "nested-call" {
		t.Fatal("nested scoped identity not replaced", got, err)
	}
	if _, ok := ExecutionIdentityFromContext(nil); ok {
		t.Fatal("nil context identity")
	}
}

func TestExecutionIdentityApprovedHandlerUsesExecutorContext(t *testing.T) {
	counter := 0
	d := definition(&counter)
	d.ReadOnly = false
	d.Handler = func(ctx context.Context, _ json.RawMessage) (runtime.ToolResult, error) {
		counter++
		identity, ok := ExecutionIdentityFromContext(ctx)
		if !ok || identity.TaskID != "task" || identity.ToolCallID != "call" {
			t.Error("approved handler lost executor identity")
		}
		return runtime.ToolResult{Effect: runtime.ConfirmedEffect}, nil
	}
	r := &Registry{}
	if err := r.Register(d); err != nil {
		t.Fatal(err)
	}
	e := Executor{Registry: r, Policy: &Policy{Default: Allow}, Authority: authorityFunc(func(ctx context.Context, _ Authorization, invoke func(context.Context) (runtime.ToolResult, error)) (runtime.ToolResult, error) {
		identity, ok := ExecutionIdentityFromContext(ctx)
		if !ok {
			t.Error("approved path lost identity")
		}
		identity.TaskID = "authority override"
		return invoke(context.WithValue(context.Background(), executionIdentityKey{}, identity))
	})}
	if _, err := e.ExecuteScoped(context.Background(), authorizedExecution()); err != nil || counter != 1 {
		t.Fatal(err, counter)
	}
}

func TestExecutionIdentityDeniedOrMalformedNeverEntersHandler(t *testing.T) {
	for _, which := range []string{"deny", "schema", "empty", "control", "long"} {
		t.Run(which, func(t *testing.T) {
			calls := 0
			r := &Registry{}
			if err := r.Register(definition(&calls)); err != nil {
				t.Fatal(err)
			}
			e := Executor{Registry: r, Policy: &Policy{Default: Allow}}
			x := authorizedExecution()
			switch which {
			case "deny":
				e.Policy.Default = Deny
			case "schema":
				x.Call.Arguments = json.RawMessage(`{"q":false}`)
			case "empty":
				x.AttemptID = ""
			case "control":
				x.TaskID = "task\n"
			case "long":
				x.SessionID = strings.Repeat("x", 129)
			}
			if _, err := e.ExecuteScoped(context.Background(), x); err == nil || calls != 0 {
				t.Fatal("invalid identity or proposal reached handler")
			}
		})
	}
}
