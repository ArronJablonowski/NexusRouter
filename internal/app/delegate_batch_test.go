package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/tools"
)

func batchExecutor(t *testing.T, reserve func(int) bool, run func(context.Context, delegateInput) (runtime.ToolResult, error)) tools.Executor {
	t.Helper()
	r := &tools.Registry{}
	if err := registerDelegateBatch(r, reserve, run); err != nil {
		t.Fatal(err)
	}
	return tools.Executor{Registry: r, Policy: &tools.Policy{Default: tools.Allow}}
}
func batchCall(raw string) providers.ToolCall {
	return providers.ToolCall{ID: "batch", Name: "delegate_batch", Arguments: json.RawMessage(raw)}
}

const batchTwo = `{"tasks":[{"prompt":"first","validation":"text"},{"prompt":"second","validation":"go_source"}]}`

func TestDelegateBatchConcurrentOrdered(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var started atomic.Int32
	ready := make(chan struct{})
	e := batchExecutor(t, func(n int) bool { return n == 2 }, func(ctx context.Context, in delegateInput) (runtime.ToolResult, error) {
		if started.Add(1) == 2 {
			close(ready)
		}
		select {
		case <-ready:
		case <-ctx.Done():
			return runtime.ToolResult{}, ctx.Err()
		}
		return runtime.ToolResult{Content: `{"prompt":"` + in.Prompt + `"}`, Effect: runtime.NoEffect}, nil
	})
	out, err := e.Execute(ctx, batchCall(batchTwo))
	if err != nil || out.Failed || out.Recoverable || out.Effect != runtime.NoEffect || out.Content != `{"results":[{"prompt":"first"},{"prompt":"second"}]}` {
		t.Fatal(out, err)
	}
}

func TestDelegateBatchRejectsBeforeReservation(t *testing.T) {
	var reservations atomic.Int32
	e := batchExecutor(t, func(n int) bool { reservations.Add(1); return false }, func(context.Context, delegateInput) (runtime.ToolResult, error) {
		t.Error("executed")
		return runtime.ToolResult{}, nil
	})
	for _, raw := range []string{`{"tasks":[]}`, strings.Replace(batchTwo, "first", " ", 1), strings.Replace(batchTwo, "first", strings.Repeat("é", 9000), 1), strings.Replace(batchTwo, `"text"`, `"invalid"`, 1), strings.Replace(batchTwo, `"tasks":`, `"extra":1,"tasks":`, 1)} {
		_, _ = e.Execute(context.Background(), batchCall(raw))
	}
	if reservations.Load() != 0 {
		t.Fatal(reservations.Load())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = e.Execute(ctx, batchCall(batchTwo))
	if reservations.Load() != 0 {
		t.Fatal("canceled reserved")
	}
	out, err := e.Execute(context.Background(), batchCall(batchTwo))
	if err != nil || !strings.Contains(out.Content, "delegate_unavailable_or_rejected") || reservations.Load() != 1 {
		t.Fatal(out, err)
	}
}

func TestDelegateBatchJoinsAndSanitizes(t *testing.T) {
	for _, mode := range []string{"panic", "error", "effect", "large", "cancel", "late_success"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var finished atomic.Int32
			var started atomic.Int32
			ready := make(chan struct{})
			e := batchExecutor(t, func(int) bool { return true }, func(ctx context.Context, in delegateInput) (runtime.ToolResult, error) {
				defer finished.Add(1)
				if started.Add(1) == 2 {
					close(ready)
				}
				<-ready
				switch mode {
				case "panic":
					panic("private")
				case "error":
					return runtime.ToolResult{}, errors.New("private")
				case "effect":
					return runtime.ToolResult{Content: `{}`, Effect: runtime.UncertainEffect}, nil
				case "large":
					return runtime.ToolResult{Content: `"` + strings.Repeat("x", 128<<10) + `"`, Effect: runtime.NoEffect}, nil
				case "cancel":
					cancel()
					<-ctx.Done()
					return runtime.ToolResult{}, ctx.Err()
				case "late_success":
					cancel()
					return runtime.ToolResult{Content: `{"untrusted_output":"private late answer"}`, Effect: runtime.NoEffect}, nil
				}
				return runtime.ToolResult{}, nil
			})
			out, err := e.Execute(ctx, batchCall(batchTwo))
			if mode == "panic" || mode == "error" || mode == "effect" || mode == "cancel" {
				if err == nil || out.Effect != runtime.UncertainEffect || out.Recoverable || finished.Load() != 2 || out.Content != "" {
					t.Fatal("uncertainty was made recoverable", out, err, finished.Load())
				}
				return
			}
			if err != nil || finished.Load() != 2 || strings.Count(out.Content, "delegate_unavailable_or_rejected") != 2 || strings.Contains(out.Content, "private") {
				t.Fatal(out, err, finished.Load())
			}
		})
	}
}

func TestDelegateBatchTypedPartialFailurePreservesOrderAndBudget(t *testing.T) {
	for _, recoverable := range []bool{false, true} {
		t.Run(fmt.Sprint(recoverable), func(t *testing.T) {
			var reserved, executed atomic.Int32
			e := batchExecutor(t, func(n int) bool { return reserved.CompareAndSwap(0, int32(n)) }, func(_ context.Context, in delegateInput) (runtime.ToolResult, error) {
				executed.Add(1)
				if in.Prompt == "second" {
					return runtime.ToolResult{Content: `{"error":"delegate_unavailable_or_rejected","reason":"invalid_output"}`, Effect: runtime.NoEffect, Failed: true, Recoverable: recoverable}, nil
				}
				return runtime.ToolResult{Content: `{"untrusted_output":"error-looking model prose is not a typed failure"}`, Effect: runtime.NoEffect}, nil
			})
			out, err := e.Execute(context.Background(), batchCall(batchTwo))
			if err != nil || !out.Failed || out.Recoverable != recoverable || out.Effect != runtime.NoEffect || out.Content != `{"results":[{"untrusted_output":"error-looking model prose is not a typed failure"},{"error":"delegate_unavailable_or_rejected","reason":"invalid_output"}]}` || reserved.Load() != 2 || executed.Load() != 2 {
				t.Fatal(out, err, reserved.Load(), executed.Load())
			}
			denied, err := e.Execute(context.Background(), batchCall(batchTwo))
			if err != nil || !denied.Failed || !denied.Recoverable || denied.Effect != runtime.NoEffect || executed.Load() != 2 {
				t.Fatal("batch repair bypassed spent call budget", denied, err, executed.Load())
			}
		})
	}
}

func TestDelegateBatchMalformedChildCannotBecomeRecoverable(t *testing.T) {
	for _, failed := range []bool{false, true} {
		for _, content := range []string{`{invalid`, `"` + strings.Repeat("x", 129<<10) + `"`} {
			e := batchExecutor(t, func(int) bool { return true }, func(context.Context, delegateInput) (runtime.ToolResult, error) {
				return runtime.ToolResult{Content: content, Effect: runtime.NoEffect, Failed: failed}, nil
			})
			out, err := e.Execute(context.Background(), batchCall(batchTwo))
			if err != nil || !out.Failed || out.Recoverable || out.Effect != runtime.NoEffect || strings.Count(out.Content, "delegate_unavailable_or_rejected") != 2 {
				t.Fatal("malformed child upgraded to repairable", out, err)
			}
		}
	}
}
