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

type authorityFunc func(context.Context, Authorization, func(context.Context) (runtime.ToolResult, error)) (runtime.ToolResult, error)

func (f authorityFunc) ExecuteApproved(c context.Context, a Authorization, invoke func(context.Context) (runtime.ToolResult, error)) (runtime.ToolResult, error) {
	return f(c, a, invoke)
}

func authorizedExecution() runtime.ToolExecution {
	return runtime.ToolExecution{TaskID: "task", SessionID: "session", TurnID: "turn", AttemptID: "attempt", Call: providers.ToolCall{ID: "call", Name: "lookup", Arguments: json.RawMessage(`{ "q": "value" }`)}}
}

func TestApprovalPreservesAmbiguousConsumptionWithoutDispatch(t *testing.T) {
	calls := 0
	d := definition(&calls)
	d.ReadOnly = false
	r := &Registry{}
	if err := r.Register(d); err != nil {
		t.Fatal(err)
	}
	authorityCalls := 0
	e := Executor{Registry: r, Policy: &Policy{Default: Allow}, Authority: authorityFunc(func(context.Context, Authorization, func(context.Context) (runtime.ToolResult, error)) (runtime.ToolResult, error) {
		authorityCalls++
		// Consumption may have committed even though its acknowledgement failed.
		// No handler dispatch is permitted, but recovery must retain uncertainty.
		return runtime.ToolResult{Content: "private database failure", Effect: runtime.UncertainEffect}, errors.New("private consume acknowledgement")
	})}
	out, err := e.ExecuteScoped(context.Background(), authorizedExecution())
	if !errors.Is(err, ErrExecution) || err.Error() != ErrExecution.Error() || out.Effect != runtime.UncertainEffect || out.Content != "" || calls != 0 || authorityCalls != 1 {
		t.Fatalf("ambiguous consumption downgraded or leaked: out=%+v err=%v handlers=%d authorities=%d", out, err, calls, authorityCalls)
	}
}

func TestApprovalAdmissionBoundaries(t *testing.T) {
	for _, name := range []string{"write_without_authority", "unscoped_write", "ask_without_authority", "deny", "invalid_arguments", "parent_deny"} {
		t.Run(name, func(t *testing.T) {
			calls, authorities := 0, 0
			d := definition(&calls)
			d.ReadOnly = name == "ask_without_authority"
			r := &Registry{}
			if err := r.Register(d); err != nil {
				t.Fatal(err)
			}
			e := Executor{Registry: r, Policy: &Policy{Default: Allow}, Authority: authorityFunc(func(ctx context.Context, _ Authorization, invoke func(context.Context) (runtime.ToolResult, error)) (runtime.ToolResult, error) {
				authorities++
				return invoke(ctx)
			})}
			x := authorizedExecution()
			want := ErrDenied
			switch name {
			case "write_without_authority":
				e.Authority = nil
			case "ask_without_authority":
				e.Authority = nil
				e.Policy.Default = Ask
			case "deny":
				e.Policy.Default = Deny
			case "parent_deny":
				e.Policy.Parent = &Policy{Default: Deny}
			case "invalid_arguments":
				x.Call.Arguments = json.RawMessage(`{"q":1}`)
				want = ErrArguments
			}
			var out runtime.ToolResult
			var err error
			if name == "unscoped_write" {
				out, err = e.Execute(context.Background(), x.Call)
			} else {
				out, err = e.ExecuteScoped(context.Background(), x)
			}
			if !errors.Is(err, want) || out.Effect != runtime.NoEffect || calls != 0 || authorities != 0 {
				t.Fatalf("out=%+v err=%v handlers=%d authorities=%d", out, err, calls, authorities)
			}
		})
	}
}

func TestApprovalExactBindingAndPolicySnapshot(t *testing.T) {
	calls := 0
	d := definition(&calls)
	d.ReadOnly = false
	d.Handler = func(context.Context, json.RawMessage) (runtime.ToolResult, error) {
		calls++
		return runtime.ToolResult{Content: "written", Effect: runtime.ConfirmedEffect}, nil
	}
	r := &Registry{}
	if err := r.Register(d); err != nil {
		t.Fatal(err)
	}
	var bindings []Authorization
	e := Executor{Registry: r, Policy: &Policy{Default: Allow, Parent: &Policy{Default: Ask}}, Authority: authorityFunc(func(ctx context.Context, a Authorization, invoke func(context.Context) (runtime.ToolResult, error)) (runtime.ToolResult, error) {
		bindings = append(bindings, a)
		return invoke(ctx)
	})}
	x := authorizedExecution()
	for range 2 {
		out, err := e.ExecuteScoped(context.Background(), x)
		if err != nil || out.Content != "written" || out.Effect != runtime.ConfirmedEffect {
			t.Fatalf("%+v %v", out, err)
		}
	}
	hash := func(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
	if len(bindings) != 2 || bindings[0] != bindings[1] {
		t.Fatal("unstable binding")
	}
	a := bindings[0]
	if a.TaskID != x.TaskID || a.SessionID != x.SessionID || a.TurnID != x.TurnID || a.AttemptID != x.AttemptID || a.ToolCallID != x.Call.ID || a.ToolName != x.Call.Name || a.Scope != d.Scope || a.ArgumentsDigest != hash(x.Call.Arguments) || a.SchemaDigest != hash(d.Tool.Parameters) || len(a.PolicyDigest) != 64 {
		t.Fatalf("incorrect binding: %+v", a)
	}
	e.Policy.Parent.Default = Allow
	if _, err := e.ExecuteScoped(context.Background(), x); err != nil {
		t.Fatal(err)
	}
	if bindings[2].PolicyDigest == a.PolicyDigest {
		t.Fatal("policy change did not change binding")
	}
}

func TestApprovalAuthorityCannotInventOrRepeatExecution(t *testing.T) {
	for _, mode := range []string{"replace_output", "twice", "never", "late", "authority_error", "authority_panic", "handler_error", "handler_panic", "cancel", "readonly_ask"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			d := definition(&calls)
			d.ReadOnly = mode == "readonly_ask"
			d.Handler = func(ctx context.Context, _ json.RawMessage) (runtime.ToolResult, error) {
				calls++
				if mode == "handler_panic" {
					panic("private panic")
				}
				if mode == "handler_error" {
					return runtime.ToolResult{Content: "private error", Effect: runtime.ConfirmedEffect}, errors.New("private")
				}
				effect := runtime.ConfirmedEffect
				if d.ReadOnly {
					effect = runtime.NoEffect
				}
				return runtime.ToolResult{Content: "actual", Effect: effect}, nil
			}
			r := &Registry{}
			if err := r.Register(d); err != nil {
				t.Fatal(err)
			}
			var late func(context.Context) (runtime.ToolResult, error)
			e := Executor{Registry: r, Policy: &Policy{Default: Ask}, Authority: authorityFunc(func(ctx context.Context, _ Authorization, invoke func(context.Context) (runtime.ToolResult, error)) (runtime.ToolResult, error) {
				if mode == "never" || mode == "late" {
					late = invoke
					return runtime.ToolResult{Content: "invented", Effect: runtime.ConfirmedEffect}, nil
				}
				if mode == "cancel" {
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					cancel()
				}
				out, err := invoke(ctx)
				if mode == "twice" {
					again, againErr := invoke(ctx)
					if !errors.Is(againErr, ErrDenied) || again.Effect != runtime.NoEffect {
						t.Error("duplicate invocation accepted")
					}
				}
				if mode == "authority_error" {
					return out, errors.New("private authority error")
				}
				if mode == "authority_panic" {
					panic("private authority panic")
				}
				if mode == "replace_output" {
					return runtime.ToolResult{Content: "invented", Effect: runtime.NoEffect}, nil
				}
				return out, err
			})}
			out, err := e.ExecuteScoped(context.Background(), authorizedExecution())
			switch mode {
			case "never", "late":
				if !errors.Is(err, ErrDenied) || out.Effect != runtime.NoEffect || calls != 0 {
					t.Fatalf("invented execution: %+v %v calls=%d", out, err, calls)
				}
				if mode == "late" {
					lateOut, lateErr := late(context.Background())
					if !errors.Is(lateErr, ErrDenied) || lateOut.Effect != runtime.NoEffect || calls != 0 {
						t.Fatal("late callback executed")
					}
				}
			case "authority_error", "authority_panic", "handler_error", "handler_panic", "cancel":
				if !errors.Is(err, ErrExecution) || out.Effect != runtime.UncertainEffect || out.Content != "" {
					t.Fatalf("unsafe failure: %+v %v", out, err)
				}
				want := 1
				if mode == "cancel" {
					want = 0
				}
				if calls != want {
					t.Fatalf("calls=%d want=%d", calls, want)
				}
			default:
				want := runtime.ConfirmedEffect
				if mode == "readonly_ask" {
					want = runtime.NoEffect
				}
				if err != nil || out.Content != "actual" || out.Effect != want || calls != 1 {
					t.Fatalf("lost handler result: %+v %v calls=%d", out, err, calls)
				}
			}
		})
	}
}
