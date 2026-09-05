package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func TestFailedHandlerResultPreservesCertainEffect(t *testing.T) {
	for _, tc := range []struct {
		readOnly bool
		effect   runtime.Effect
	}{{true, runtime.NoEffect}, {false, runtime.NoEffect}, {false, runtime.ConfirmedEffect}} {
		t.Run(fmt.Sprint(tc.readOnly, tc.effect), func(t *testing.T) {
			calls := 0
			d := definition(&calls)
			d.ReadOnly = tc.readOnly
			want := runtime.ToolResult{Content: `{"error":"certain_failure"}`, Effect: tc.effect, Failed: true}
			d.Handler = func(context.Context, json.RawMessage) (runtime.ToolResult, error) { calls++; return want, nil }
			r := &Registry{}
			if err := r.Register(d); err != nil {
				t.Fatal(err)
			}
			authorities := 0
			e := Executor{Registry: r, Policy: &Policy{Default: Allow}, Authority: authorityFunc(func(ctx context.Context, _ Authorization, invoke func(context.Context) (runtime.ToolResult, error)) (runtime.ToolResult, error) {
				authorities++
				return invoke(ctx)
			})}
			got, err := e.ExecuteScoped(context.Background(), authorizedExecution())
			if err != nil || got != want || calls != 1 || (!tc.readOnly && authorities != 1) || (tc.readOnly && authorities != 0) {
				t.Fatal(got, err, calls, authorities)
			}
		})
	}
}

func TestThrownHandlerErrorStillUncertain(t *testing.T) {
	for _, readOnly := range []bool{true, false} {
		calls := 0
		d := definition(&calls)
		d.ReadOnly = readOnly
		d.Handler = func(context.Context, json.RawMessage) (runtime.ToolResult, error) {
			return runtime.ToolResult{Content: "private", Effect: runtime.NoEffect, Failed: true}, errors.New("private")
		}
		r := &Registry{}
		if err := r.Register(d); err != nil {
			t.Fatal(err)
		}
		e := Executor{Registry: r, Policy: &Policy{Default: Allow}, Authority: authorityFunc(func(ctx context.Context, _ Authorization, invoke func(context.Context) (runtime.ToolResult, error)) (runtime.ToolResult, error) {
			return invoke(ctx)
		})}
		got, err := e.ExecuteScoped(context.Background(), authorizedExecution())
		if !errors.Is(err, ErrExecution) || got.Effect != runtime.UncertainEffect || got.Content != "" {
			t.Fatal(got, err)
		}
	}
}
