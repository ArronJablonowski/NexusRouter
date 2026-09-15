package toolgate

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/approvals"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/tools"
)

func fixture(t *testing.T) (*Gate, tools.Authorization) {
	t.Helper()
	s, err := telemetry.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	for i, kind := range []runtime.Kind{runtime.TaskStarted, runtime.TurnStarted, runtime.TurnCompleted, runtime.ToolStarted} {
		e := runtime.Event{Version: 1, ID: string(kind), TaskID: "task", SessionID: "session", CorrelationID: "task", Sequence: int64(i + 1), Time: time.Now().UTC(), Kind: kind}
		if i > 0 {
			e.TurnID, e.AttemptID = "turn", "attempt"
		}
		if kind == runtime.TurnCompleted {
			e.Data.ToolCalls = []providers.ToolCall{{ID: "call", Name: "write_file", Arguments: []byte(`{}`)}}
		}
		if kind == runtime.ToolStarted {
			e.Data = runtime.Data{ToolCallID: "call", ToolName: "write_file", Effect: runtime.UncertainEffect}
		}
		if err := s.Append(context.Background(), int64(i), e); err != nil {
			t.Fatal(err)
		}
	}
	a := tools.Authorization{TaskID: "task", SessionID: "session", TurnID: "turn", AttemptID: "attempt", ToolCallID: "call", ToolName: "write_file", Scope: "workspace", ArgumentsDigest: strings.Repeat("a", 64), SchemaDigest: strings.Repeat("b", 64), PolicyDigest: strings.Repeat("c", 64)}
	return &Gate{Store: s, Review: func(context.Context, approvals.Request) (string, bool, error) { return "operator", true, nil }}, a
}

func TestApprovalConsumesExactlyOnce(t *testing.T) {
	g, a := fixture(t)
	var request approvals.Request
	g.Review = func(_ context.Context, r approvals.Request) (string, bool, error) {
		request = r
		return "operator", true, nil
	}
	var calls atomic.Int32
	handler := func(ctx context.Context) (runtime.ToolResult, error) {
		calls.Add(1)
		consumed, ok := tools.ConsumedApprovalFromContext(ctx)
		if !ok || consumed.ID != request.ID {
			t.Errorf("consumed approval context=%+v ok=%v request=%s", consumed, ok, request.ID)
		}
		return runtime.ToolResult{Content: "done", Effect: runtime.ConfirmedEffect}, nil
	}
	out, err := g.ExecuteApproved(context.Background(), a, handler)
	if err != nil || out.Content != "done" || calls.Load() != 1 {
		t.Fatal(out, err, calls.Load())
	}
	r, err := g.Store.ReadApproval(context.Background(), request.ID)
	if err != nil || r.State != approvals.Consumed || r.Request.ArgumentsDigest != a.ArgumentsDigest {
		t.Fatal(r, err)
	}
	if _, err = g.ExecuteApproved(context.Background(), a, handler); !errors.Is(err, tools.ErrDenied) || calls.Load() != 1 {
		t.Fatal(err, calls.Load())
	}
	leases, err := g.Store.InspectLeases(context.Background(), a.Scope)
	if err != nil || len(leases) != 0 {
		t.Fatal(leases, err)
	}
}

func TestConcurrentDuplicateCallsDispatchOnce(t *testing.T) {
	g, a := fixture(t)
	var calls, wins atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			_, err := g.ExecuteApproved(context.Background(), a, func(context.Context) (runtime.ToolResult, error) {
				calls.Add(1)
				return runtime.ToolResult{Effect: runtime.ConfirmedEffect}, nil
			})
			if err == nil {
				wins.Add(1)
			} else if !errors.Is(err, tools.ErrDenied) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 1 || wins.Load() != 1 {
		t.Fatal(calls.Load(), wins.Load())
	}
}

func TestReviewAndIdentityFailuresNeverDispatch(t *testing.T) {
	for _, which := range []string{"denied", "review_error", "review_panic", "actor", "session", "attempt", "digest", "canceled", "cancel_review", "lease_busy"} {
		t.Run(which, func(t *testing.T) {
			g, a := fixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var captured approvals.Request
			g.Review = func(_ context.Context, r approvals.Request) (string, bool, error) {
				captured = r
				switch which {
				case "denied":
					return "operator", false, nil
				case "review_error":
					return "", false, errors.New("secret")
				case "review_panic":
					panic("secret")
				case "actor":
					return "", true, nil
				case "cancel_review":
					cancel()
				}
				return "operator", true, nil
			}
			switch which {
			case "session":
				a.SessionID = "other"
			case "attempt":
				a.AttemptID = "other"
			case "digest":
				a.ArgumentsDigest = "bad"
			case "canceled":
				cancel()
			case "lease_busy":
				if _, err := g.Store.AcquireLease(ctx, a.TaskID, "other", a.Scope, true, time.Now().UTC(), time.Minute); err != nil {
					t.Fatal(err)
				}
			}
			called := false
			out, err := g.ExecuteApproved(ctx, a, func(context.Context) (runtime.ToolResult, error) { called = true; return runtime.ToolResult{}, nil })
			if !errors.Is(err, tools.ErrDenied) || out.Effect != runtime.NoEffect || called {
				t.Fatal(out, err, called)
			}
			if which == "denied" {
				r, err := g.Store.ReadApproval(context.Background(), captured.ID)
				if err != nil || r.State != approvals.Denied {
					t.Fatal(r, err)
				}
			}
		})
	}
}

func TestCancellationWaitsForHandlerBeforeRelease(t *testing.T) {
	g, a := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started, stopping, finish, done := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		out, err := g.ExecuteApproved(ctx, a, func(ctx context.Context) (runtime.ToolResult, error) {
			close(started)
			<-ctx.Done()
			close(stopping)
			<-finish
			return runtime.ToolResult{Content: "secret", Effect: runtime.ConfirmedEffect}, nil
		})
		if !errors.Is(err, tools.ErrExecution) || out.Effect != runtime.UncertainEffect || out.Content != "" {
			t.Error(out, err)
		}
	}()
	<-started
	cancel()
	<-stopping
	leases, err := g.Store.InspectLeases(context.Background(), a.Scope)
	if err != nil || len(leases) != 1 {
		t.Fatal(leases, err)
	}
	select {
	case <-done:
		t.Fatal("abandoned handler")
	default:
	}
	close(finish)
	<-done
	leases, err = g.Store.InspectLeases(context.Background(), a.Scope)
	if err != nil || len(leases) != 0 {
		t.Fatal(leases, err)
	}
}

func TestHandlerFailuresAreUncertainAndSanitized(t *testing.T) {
	for _, which := range []string{"error", "panic", "invalid_effect", "invalid_utf8", "oversize"} {
		t.Run(which, func(t *testing.T) {
			g, a := fixture(t)
			out, err := g.ExecuteApproved(context.Background(), a, func(context.Context) (runtime.ToolResult, error) {
				r := runtime.ToolResult{Content: "secret", Effect: runtime.ConfirmedEffect}
				switch which {
				case "error":
					return r, errors.New("secret")
				case "panic":
					panic("secret")
				case "invalid_effect":
					r.Effect = "bad"
				case "invalid_utf8":
					r.Content = string([]byte{255})
				case "oversize":
					r.Content = strings.Repeat("s", (1<<20)+1)
				}
				return r, nil
			})
			if !errors.Is(err, tools.ErrExecution) || out.Effect != runtime.UncertainEffect || out.Content != "" {
				t.Fatal(out, err)
			}
			leases, err := g.Store.InspectLeases(context.Background(), a.Scope)
			if err != nil || len(leases) != 0 {
				t.Fatal(leases, err)
			}
		})
	}
}

func TestLostLeaseCancelsCooperativeHandler(t *testing.T) {
	g, a := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := g.ExecuteApproved(ctx, a, func(work context.Context) (runtime.ToolResult, error) {
		leases, err := g.Store.InspectLeases(work, a.Scope)
		if err != nil || len(leases) != 1 {
			t.Fatal(leases, err)
		}
		// Simulate external lease revocation; the heartbeat must reject it.
		if err = g.Store.ReleaseLease(work, leases[0].Token, leases[0].Owner); err != nil {
			t.Fatal(err)
		}
		<-work.Done()
		return runtime.ToolResult{Effect: runtime.ConfirmedEffect}, nil
	})
	if ctx.Err() != nil || !errors.Is(err, tools.ErrExecution) || out.Effect != runtime.UncertainEffect {
		t.Fatal(out, err, ctx.Err())
	}
}

func TestShortHandlerLostLeaseRejectsResult(t *testing.T) {
	g, a := fixture(t)
	out, err := g.ExecuteApproved(context.Background(), a, func(ctx context.Context) (runtime.ToolResult, error) {
		leases, err := g.Store.InspectLeases(ctx, a.Scope)
		if err != nil || len(leases) != 1 {
			t.Fatal(leases, err)
		}
		if err = g.Store.ReleaseLease(ctx, leases[0].Token, leases[0].Owner); err != nil {
			t.Fatal(err)
		}
		return runtime.ToolResult{Content: "must not escape", Effect: runtime.ConfirmedEffect}, nil
	})
	if !errors.Is(err, tools.ErrExecution) || out.Effect != runtime.UncertainEffect || out.Content != "" {
		t.Fatal(out, err)
	}
}
