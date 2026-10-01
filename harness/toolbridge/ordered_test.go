package toolbridge

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func orderedFixture(t *testing.T, timeout time.Duration, fn Invoke) *Bridge {
	t.Helper()
	b, e := NewOrdered(context.Background(), 4, timeout, fn)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(b.Close)
	for _, id := range []string{"first", "second", "third"} {
		x := proposal()
		x.Call.ID = id
		if e = b.Register(x); e != nil {
			t.Fatal(e)
		}
	}
	return b
}
func waitBridgeRequest(t *testing.T, b *Bridge, id string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		b.mu.Lock()
		started := b.calls[id].started
		b.mu.Unlock()
		if started {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("request did not enter bridge", id)
}
func TestOrderedReverseArrivalsAndDuplicateResults(t *testing.T) {
	var mu sync.Mutex
	var effects []string
	b := orderedFixture(t, time.Second, func(_ context.Context, x runtime.ToolExecution) (runtime.ToolResult, error) {
		mu.Lock()
		effects = append(effects, x.Call.ID)
		mu.Unlock()
		return runtime.ToolResult{Content: x.Call.ID, Effect: runtime.NoEffect}, nil
	})
	result := make(chan error, 4)
	for _, id := range []string{"third", "second", "third"} {
		go func() {
			r, e := b.execute(context.Background(), id)
			if e == nil && r.Content != id {
				e = errors.New("wrong cached result")
			}
			result <- e
		}()
		waitBridgeRequest(t, b, id)
	}
	mu.Lock()
	before := len(effects)
	mu.Unlock()
	if before != 0 {
		t.Fatal("speculative or out-of-order effect")
	}
	// A duplicate registration must not move its position in the chain.
	x := proposal()
	x.Call.ID = "first"
	if e := b.Register(x); e != nil {
		t.Fatal(e)
	}
	_, e := b.execute(context.Background(), "first")
	if e != nil {
		t.Fatal(e)
	}
	for range 3 {
		if e := <-result; e != nil {
			t.Fatal(e)
		}
	}
	for _, id := range []string{"first", "second", "third"} {
		if _, e := b.execute(context.Background(), id); e != nil {
			t.Fatal(e)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(effects, []string{"first", "second", "third"}) {
		t.Fatal(effects)
	}
}
func TestOrderedMissingPredecessorCancellationAndClose(t *testing.T) {
	for _, mode := range []string{"cancel", "timeout", "close"} {
		t.Run(mode, func(t *testing.T) {
			var mu sync.Mutex
			effects := 0
			timeout := time.Second
			if mode == "timeout" {
				timeout = 25 * time.Millisecond
			}
			b := orderedFixture(t, timeout, func(context.Context, runtime.ToolExecution) (runtime.ToolResult, error) {
				mu.Lock()
				effects++
				mu.Unlock()
				return runtime.ToolResult{Effect: runtime.NoEffect}, nil
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, e := b.execute(ctx, "second"); done <- e }()
			waitBridgeRequest(t, b, "second")
			if mode == "cancel" {
				cancel()
			}
			if mode == "close" {
				b.Close()
			}
			select {
			case e := <-done:
				if e == nil {
					t.Fatal("waiting request succeeded")
				}
			case <-time.After(time.Second):
				t.Fatal("unbounded wait")
			}
			for _, id := range []string{"first", "second", "third"} {
				if _, e := b.execute(context.Background(), id); e == nil {
					t.Fatal("crossed canceled-order fence", id)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if effects != 0 {
				t.Fatal("unrequested predecessor ran", effects)
			}
		})
	}
}
func TestOrderedTerminalPredecessorFencesWaitingEffects(t *testing.T) {
	for _, mode := range []string{"end", "failed", "recoverable"} {
		t.Run(mode, func(t *testing.T) {
			var mu sync.Mutex
			var effects []string
			b := orderedFixture(t, time.Second, func(_ context.Context, x runtime.ToolExecution) (runtime.ToolResult, error) {
				mu.Lock()
				effects = append(effects, x.Call.ID)
				mu.Unlock()
				r := runtime.ToolResult{Content: x.Call.ID, Effect: runtime.NoEffect}
				if x.Call.ID == "first" {
					r.EndToolUse = mode == "end"
					r.Failed = mode != "end"
					r.Recoverable = mode == "recoverable"
				}
				return r, nil
			})
			done := make(chan error, 1)
			go func() { _, e := b.execute(context.Background(), "second"); done <- e }()
			waitBridgeRequest(t, b, "second")
			_, firstErr := b.execute(context.Background(), "first")
			if (firstErr != nil) != (mode == "failed") {
				t.Fatal(firstErr)
			}
			e := <-done
			if (e == nil) != (mode == "recoverable") {
				t.Fatal(mode, e)
			}
			mu.Lock()
			defer mu.Unlock()
			want := []string{"first"}
			if mode == "recoverable" {
				want = append(want, "second")
			}
			if !reflect.DeepEqual(effects, want) {
				t.Fatal(effects)
			}
		})
	}
}

func TestOrderedCanceledFirstCallFencesSuccessor(t *testing.T) {
	effects := 0
	b := orderedFixture(t, time.Second, func(context.Context, runtime.ToolExecution) (runtime.ToolResult, error) {
		effects++
		return runtime.ToolResult{Effect: runtime.NoEffect}, nil
	})
	// Simulate cancellation after admission but before the first slot acquisition.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	first := b.calls["first"]
	if _, err := b.invokeInOrder(ctx, first); err == nil {
		t.Fatal("canceled call succeeded")
	}
	close(first.done)
	if _, err := b.execute(context.Background(), "second"); err == nil {
		t.Fatal("successor crossed canceled first call")
	}
	if effects != 0 {
		t.Fatal("unexpected effect", effects)
	}
}
