package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func definition(counter *int) Definition {
	return Definition{Tool: providers.Tool{Name: "lookup", Parameters: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string","minLength":1}},"required":["q"],"additionalProperties":false}`)}, Scope: "project", ReadOnly: true, Handler: func(context.Context, json.RawMessage) (runtime.ToolResult, error) {
		*counter++
		return runtime.ToolResult{Content: "ok", Effect: runtime.NoEffect}, nil
	}}
}

func TestSchemaBoundary(t *testing.T) {
	count := 0
	r := &Registry{}
	if err := r.Register(definition(&count)); err != nil {
		t.Fatal(err)
	}
	e := Executor{Registry: r, Policy: &Policy{Default: Allow}}
	for _, raw := range []string{`{}`, `{"q":1}`, `{"q":""}`, `{"q":"a","extra":true}`, `{"q":"a","q":"b"}`, `{"q":"a"} {}`, `null`, `[]`} {
		if _, err := e.Execute(context.Background(), providers.ToolCall{ID: "id", Name: "lookup", Arguments: json.RawMessage(raw)}); !errors.Is(err, ErrArguments) {
			t.Fatalf("%s: %v", raw, err)
		}
	}
	if count != 0 {
		t.Fatal("invalid arguments dispatched")
	}
	out, err := e.Execute(context.Background(), providers.ToolCall{ID: "id", Name: "lookup", Arguments: json.RawMessage(`{"q":"a"}`)})
	if err != nil || out.Content != "ok" || count != 1 {
		t.Fatalf("%+v %v", out, err)
	}
	if err := r.Register(definition(&count)); err == nil {
		t.Fatal("duplicate registration accepted")
	}
	catalog := r.Catalog()
	catalog[0].Parameters[0] = 'x'
	if r.Catalog()[0].Parameters[0] != '{' {
		t.Fatal("catalog mutated registry")
	}
}

func TestPolicyInheritance(t *testing.T) {
	for _, tc := range []struct {
		policy *Policy
		want   Decision
	}{
		{nil, Deny}, {&Policy{}, Deny}, {&Policy{Default: Allow}, Allow},
		{&Policy{Default: Allow, Parent: &Policy{Default: Deny}}, Deny},
		{&Policy{Default: Allow, Parent: &Policy{Default: Ask}}, Ask},
		{&Policy{Default: Allow, Rules: []Rule{{"lookup", "project", Deny}, {"*", "*", Allow}}}, Deny},
		{&Policy{Default: Deny, Rules: []Rule{{"lookup", "project", Allow}}}, Allow},
		{&Policy{Default: Allow, Rules: []Rule{{"lookup", "project", Ask}}}, Ask},
	} {
		if got := tc.policy.Decide("lookup", "project"); got != tc.want {
			t.Fatalf("got %s want %s", got, tc.want)
		}
	}
}

func TestDenialBeforeDispatch(t *testing.T) {
	for _, name := range []string{"ask", "deny", "write", "unknown", "cancel"} {
		t.Run(name, func(t *testing.T) {
			count := 0
			r := &Registry{}
			d := definition(&count)
			if name == "write" {
				d.ReadOnly = false
			}
			if err := r.Register(d); err != nil {
				t.Fatal(err)
			}
			p := &Policy{Default: Allow}
			if name == "ask" {
				p.Default = Ask
			}
			if name == "deny" {
				p.Default = Deny
			}
			e := Executor{Registry: r, Policy: p}
			call := providers.ToolCall{ID: "id", Name: "lookup", Arguments: json.RawMessage(`{"q":"a"}`)}
			if name == "unknown" {
				call.Name = "shell"
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if name == "cancel" {
				cancel()
			}
			if _, err := e.Execute(ctx, call); err == nil || count != 0 {
				t.Fatalf("dispatch count %d, error %v", count, err)
			}
		})
	}
}

func TestExternalSchemaAndHandlerFailures(t *testing.T) {
	count := 0
	d := definition(&count)
	d.Tool.Parameters = json.RawMessage(`{"$ref":"https://example.invalid/schema.json"}`)
	if err := (&Registry{}).Register(d); !errors.Is(err, ErrDefinition) {
		t.Fatal(err)
	}
	for _, panicHandler := range []bool{false, true} {
		r := &Registry{}
		d := definition(&count)
		d.Handler = func(context.Context, json.RawMessage) (runtime.ToolResult, error) {
			if panicHandler {
				panic("private data")
			}
			return runtime.ToolResult{}, errors.New("private data")
		}
		if err := r.Register(d); err != nil {
			t.Fatal(err)
		}
		out, err := (Executor{Registry: r, Policy: &Policy{Default: Allow}}).Execute(context.Background(), providers.ToolCall{ID: "id", Name: "lookup", Arguments: json.RawMessage(`{"q":"a"}`)})
		if err != ErrExecution || out.Effect != runtime.UncertainEffect || out.Content != "" {
			t.Fatalf("%+v %v", out, err)
		}
	}
}
