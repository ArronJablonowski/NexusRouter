package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

type readAuthorityFunc func(context.Context, runtime.ToolExecution, string, func(context.Context) (runtime.ToolResult, error)) (runtime.ToolResult, error)

func (f readAuthorityFunc) ExecuteRead(ctx context.Context, x runtime.ToolExecution, scope string, call func(context.Context) (runtime.ToolResult, error)) (runtime.ToolResult, error) {
	return f(ctx, x, scope, call)
}

func TestReadAuthoritySingleUseScopeIdentityAndOutput(t *testing.T) {
	calls := 0
	d := definition(&calls)
	d.Handler = func(ctx context.Context, args json.RawMessage) (runtime.ToolResult, error) {
		calls++
		identity, ok := ExecutionIdentityFromContext(ctx)
		if !ok || identity.TaskID != "task" || string(args) != string(authorizedExecution().Call.Arguments) {
			t.Error("reader changed admitted identity or arguments")
		}
		return runtime.ToolResult{Content: "actual", Effect: runtime.NoEffect, Failed: true, Recoverable: true}, nil
	}
	r := &Registry{}
	if err := r.Register(d); err != nil {
		t.Fatal(err)
	}
	var late func(context.Context) (runtime.ToolResult, error)
	readerCalls := 0
	e := Executor{Registry: r, Policy: &Policy{Default: Allow}, Reader: readAuthorityFunc(func(ctx context.Context, x runtime.ToolExecution, scope string, call func(context.Context) (runtime.ToolResult, error)) (runtime.ToolResult, error) {
		readerCalls++
		if scope != d.Scope {
			t.Error("not registered scope")
		}
		x.Call.Arguments[0] = 'x'
		late = call
		out, err := call(context.Background())
		if err != nil || out.Content != "actual" {
			t.Error("callback failed", err)
		}
		if _, err = call(ctx); !errors.Is(err, ErrDenied) {
			t.Error("repeated callback accepted")
		}
		return runtime.ToolResult{Content: "forged", Effect: runtime.ConfirmedEffect}, nil
	})}
	if _, err := e.Execute(context.Background(), authorizedExecution().Call); !errors.Is(err, ErrDenied) || readerCalls != 0 {
		t.Fatal("unscoped read acquired authority")
	}
	out, err := e.ExecuteScoped(context.Background(), authorizedExecution())
	if err != nil || out.Content != "actual" || !out.Failed || !out.Recoverable || out.Effect != runtime.NoEffect || calls != 1 || readerCalls != 1 {
		t.Fatal("read result replaced", out, err)
	}
	if _, err = late(context.Background()); !errors.Is(err, ErrDenied) || calls != 1 {
		t.Fatal("late callback executed")
	}
}

func TestReadAuthorityCannotHideErrorsOrCancellation(t *testing.T) {
	for _, mode := range []string{"handler_error", "handler_panic", "confirmed", "uncertain", "flags", "gate_error", "gate_panic", "cancel", "no_callback"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			d := definition(&calls)
			d.Handler = func(context.Context, json.RawMessage) (runtime.ToolResult, error) {
				calls++
				out := runtime.ToolResult{Content: "private", Effect: runtime.NoEffect}
				switch mode {
				case "handler_error":
					return out, errors.New("private")
				case "handler_panic":
					panic("private")
				case "confirmed":
					out.Effect = runtime.ConfirmedEffect
				case "uncertain":
					out.Effect = runtime.UncertainEffect
				case "flags":
					out.Recoverable = true
				}
				return out, nil
			}
			r := &Registry{}
			if err := r.Register(d); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			e := Executor{Registry: r, Policy: &Policy{Default: Allow}, Reader: readAuthorityFunc(func(ctx context.Context, _ runtime.ToolExecution, _ string, call func(context.Context) (runtime.ToolResult, error)) (runtime.ToolResult, error) {
				if mode == "no_callback" {
					return runtime.ToolResult{Effect: runtime.NoEffect}, nil
				}
				_, _ = call(ctx)
				switch mode {
				case "gate_error":
					return runtime.ToolResult{}, errors.New("private")
				case "gate_panic":
					panic("private")
				case "cancel":
					cancel()
				}
				return runtime.ToolResult{Effect: runtime.NoEffect}, nil
			})}
			out, err := e.ExecuteScoped(ctx, authorizedExecution())
			if err == nil || out.Content != "" {
				t.Fatal("invalid read accepted")
			}
			if mode == "no_callback" {
				if calls != 0 || out.Effect != runtime.NoEffect {
					t.Fatal("no callback changed effect")
				}
			} else if calls != 1 || out.Effect != runtime.UncertainEffect {
				t.Fatal("failure downgraded", out)
			}
		})
	}
}

func TestReadAuthorityNotUsedForAskOrDeniedSchema(t *testing.T) {
	calls := 0
	d := definition(&calls)
	r := &Registry{}
	if err := r.Register(d); err != nil {
		t.Fatal(err)
	}
	readerCalls, approved := 0, 0
	e := Executor{Registry: r, Policy: &Policy{Default: Ask}, Reader: readAuthorityFunc(func(context.Context, runtime.ToolExecution, string, func(context.Context) (runtime.ToolResult, error)) (runtime.ToolResult, error) {
		readerCalls++
		return runtime.ToolResult{}, ErrDenied
	}), Authority: authorityFunc(func(ctx context.Context, _ Authorization, call func(context.Context) (runtime.ToolResult, error)) (runtime.ToolResult, error) {
		approved++
		return call(ctx)
	})}
	if _, err := e.ExecuteScoped(context.Background(), authorizedExecution()); err != nil || approved != 1 || readerCalls != 0 {
		t.Fatal("ask bypassed exclusive authority", err)
	}
	e.Policy = &Policy{Default: Allow}
	x := authorizedExecution()
	x.Call.Arguments = []byte(`{}`)
	if _, err := e.ExecuteScoped(context.Background(), x); !errors.Is(err, ErrArguments) || readerCalls != 0 {
		t.Fatal("invalid schema reached reader", err)
	}
}

func TestReadAuthorityJoinsStartedCallback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	started, finish, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	calls := 0
	d := definition(&calls)
	d.Handler = func(context.Context, json.RawMessage) (runtime.ToolResult, error) {
		close(started)
		select {
		case <-finish:
		case <-ctx.Done():
			return runtime.ToolResult{}, ctx.Err()
		}
		return runtime.ToolResult{Content: "joined", Effect: runtime.NoEffect}, nil
	}
	r := &Registry{}
	if err := r.Register(d); err != nil {
		t.Fatal(err)
	}
	e := Executor{Registry: r, Policy: &Policy{Default: Allow}, Reader: readAuthorityFunc(func(ctx context.Context, _ runtime.ToolExecution, _ string, call func(context.Context) (runtime.ToolResult, error)) (runtime.ToolResult, error) {
		go func() { _, _ = call(ctx) }()
		select {
		case <-started:
		case <-ctx.Done():
			return runtime.ToolResult{}, ctx.Err()
		}
		close(returned)
		return runtime.ToolResult{Effect: runtime.NoEffect}, nil
	})}
	done := make(chan error, 1)
	go func() {
		out, err := e.ExecuteScoped(ctx, authorizedExecution())
		if err == nil && out.Content != "joined" {
			err = errors.New("lost joined output")
		}
		done <- err
	}()
	select {
	case <-returned:
	case <-ctx.Done():
		t.Fatal("authority did not return")
	}
	select {
	case <-done:
		t.Fatal("executor abandoned started callback")
	default:
	}
	close(finish)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("executor did not join")
	}
}

func TestReadAuthorityCancellationDiagnosticRequiresExactGateAttestation(t *testing.T) {
	for _, mode := range []string{"valid", "forged", "gate_error", "joined_error", "wrong_cancel", "handler_error", "not_canceled", "nonfailed"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			d := definition(&calls)
			d.Handler = func(context.Context, json.RawMessage) (runtime.ToolResult, error) {
				calls++
				if mode != "not_canceled" {
					cancel()
				}
				out := runtime.ToolResult{Content: "bounded diagnostic", Effect: runtime.NoEffect, Failed: mode != "nonfailed", Recoverable: mode != "nonfailed"}
				if mode == "handler_error" {
					return out, errors.New("private")
				}
				return out, nil
			}
			r := &Registry{}
			if err := r.Register(d); err != nil {
				t.Fatal(err)
			}
			e := Executor{Registry: r, Policy: &Policy{Default: Allow}, Reader: readAuthorityFunc(func(ctx context.Context, _ runtime.ToolExecution, _ string, call func(context.Context) (runtime.ToolResult, error)) (runtime.ToolResult, error) {
				out, _ := call(ctx)
				out.Recoverable = false
				if mode == "forged" {
					out.Content = "gate fabricated diagnostic"
				}
				if mode == "gate_error" {
					return out, ErrExecution
				}
				if mode == "wrong_cancel" {
					return out, context.DeadlineExceeded
				}
				if mode == "joined_error" {
					return out, errors.Join(context.Canceled, ErrExecution)
				}
				return out, context.Canceled
			})}
			out, err := e.ExecuteScoped(ctx, authorizedExecution())
			if mode == "valid" {
				if !errors.Is(err, context.Canceled) || out.Content != "bounded diagnostic" || !out.Failed || out.Recoverable || out.Effect != runtime.NoEffect {
					t.Fatal("known failure diagnostic lost", out, err)
				}
			} else if !errors.Is(err, ErrExecution) || out.Content != "" || out.Effect != runtime.UncertainEffect {
				t.Fatal("unattested cancellation retained diagnostic", out, err)
			}
			if calls != 1 {
				t.Fatal("callback repeated")
			}
		})
	}
}
