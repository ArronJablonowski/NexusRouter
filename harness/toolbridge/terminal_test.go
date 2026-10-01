package toolbridge

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func TestTerminalCallFencesConcurrentAndLaterCalls(t *testing.T) {
	for _, kind := range []string{"end_tool_use", "failed", "error", "invalid_recoverable"} {
		t.Run(kind, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			b := bridge(t, func(context.Context, runtime.ToolExecution) (runtime.ToolResult, error) {
				calls.Add(1)
				close(entered)
				<-release
				result := runtime.ToolResult{Content: "result", Effect: runtime.NoEffect}
				switch kind {
				case "end_tool_use":
					result.EndToolUse = true
				case "failed":
					result.Failed = true
				case "error":
					return result, errors.New("unavailable")
				case "invalid_recoverable":
					result.Recoverable = true
				}
				return result, nil
			})
			x := proposal()
			x.Call.ID = "second"
			if err := b.Register(x); err != nil {
				t.Fatal(err)
			}
			first := make(chan error, 1)
			second := make(chan error, 1)
			go func() { _, err := b.execute(context.Background(), "call"); first <- err }()
			<-entered
			go func() { _, err := b.execute(context.Background(), "second"); second <- err }()
			// The second request can reach its queue before or after the first terminal;
			// either ordering must observe the same run-wide execution fence.
			close(release)
			err := <-first
			if (err == nil) != (kind == "end_tool_use") {
				t.Fatalf("first result %v", err)
			}
			if err = <-second; err == nil {
				t.Fatal("second call crossed terminal fence")
			}
			if _, err = b.execute(context.Background(), "second"); err == nil {
				t.Fatal("later retry crossed terminal fence")
			}
			if calls.Load() != 1 {
				t.Fatal("executed after terminal")
			}
			if kind == "end_tool_use" {
				w := request(b, `{"call_id":"call"}`)
				if w.Code != 200 || !strings.Contains(w.Body.String(), `"end_tool_use":true`) {
					t.Fatalf("terminal result not replayed: %d %s", w.Code, w.Body.String())
				}
			}
		})
	}
}
func TestRecoverableNoEffectFailureAllowsNewProposal(t *testing.T) {
	var calls atomic.Int32
	b := bridge(t, func(_ context.Context, x runtime.ToolExecution) (runtime.ToolResult, error) {
		calls.Add(1)
		if x.Call.ID == "call" {
			return runtime.ToolResult{Content: "correct arguments", Effect: runtime.NoEffect, Failed: true, Recoverable: true, EndToolUse: true}, nil
		}
		return runtime.ToolResult{Content: "corrected", Effect: runtime.NoEffect}, nil
	})
	x := proposal()
	x.Call.ID = "second"
	if err := b.Register(x); err != nil {
		t.Fatal(err)
	}
	w := request(b, `{"call_id":"call"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"failed":true`) || !strings.Contains(w.Body.String(), `"end_tool_use":false`) {
		t.Fatal(w.Body.String())
	}
	if _, err := b.execute(context.Background(), "second"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatal("new proposal not executed")
	}
}
func TestQueuedCallCancellationDoesNotExecute(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	b := bridge(t, func(context.Context, runtime.ToolExecution) (runtime.ToolResult, error) {
		calls.Add(1)
		close(entered)
		<-release
		return runtime.ToolResult{Effect: runtime.NoEffect}, nil
	})
	x := proposal()
	x.Call.ID = "second"
	if err := b.Register(x); err != nil {
		t.Fatal(err)
	}
	first := make(chan struct{})
	go func() { defer close(first); _, _ = b.execute(context.Background(), "call") }()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	second := make(chan error, 1)
	go func() { _, err := b.execute(ctx, "second"); second <- err }()
	// Ensure the call entered the bridge before canceling so its failed attempt
	// is cached. This is state observation, not a timing assumption.
	deadline := time.After(time.Second)
	for {
		b.mu.Lock()
		started := b.calls["second"].started
		b.mu.Unlock()
		if started {
			break
		}
		select {
		case <-deadline:
			close(release)
			cancel()
			t.Fatal("second call not admitted")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	err := <-second
	if !errors.Is(err, ErrUncertain) {
		t.Error(err)
	}
	close(release)
	<-first
	if _, err = b.execute(context.Background(), "second"); !errors.Is(err, ErrUncertain) {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("canceled queued call executed")
	}
}
