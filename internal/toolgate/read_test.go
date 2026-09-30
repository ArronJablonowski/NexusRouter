package toolgate

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/tools"
)

func readFixture(t *testing.T) (*Gate, []runtime.ToolExecution) {
	return readFixtureAt(t, filepath.Join(t.TempDir(), "read.db"))
}

func readFixtureAt(t *testing.T, path string) (*Gate, []runtime.ToolExecution) {
	t.Helper()
	s, err := telemetry.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	var xs []runtime.ToolExecution
	for _, task := range []string{"first", "second", "writer"} {
		x := runtime.ToolExecution{TaskID: task, SessionID: task, TurnID: "turn", AttemptID: "attempt", Call: providers.ToolCall{ID: "call", Name: "read_file", Arguments: []byte(`{}`)}}
		for i, kind := range []runtime.Kind{runtime.TaskStarted, runtime.TurnStarted, runtime.TurnCompleted, runtime.ToolStarted} {
			e := runtime.Event{Version: 1, ID: task + string(kind), TaskID: task, SessionID: task, CorrelationID: task, Sequence: int64(i + 1), Time: time.Now().UTC(), Kind: kind}
			if i > 0 {
				e.TurnID, e.AttemptID = x.TurnID, x.AttemptID
			}
			if kind == runtime.TurnCompleted {
				e.Data.ToolCalls = []providers.ToolCall{x.Call}
			}
			if kind == runtime.ToolStarted {
				e.Data = runtime.Data{ToolCallID: x.Call.ID, ToolName: x.Call.Name, ToolBehavior: runtime.BehaviorReadOnly, Effect: runtime.UncertainEffect}
			}
			if err := s.Append(context.Background(), int64(i), e); err != nil {
				t.Fatal(err)
			}
		}
		xs = append(xs, x)
	}
	return &Gate{Store: s}, xs
}

func TestReadGateRejectsExpiredOwnershipBeforeAcceptingResult(t *testing.T) {
	path := filepath.Join(t.TempDir(), "expiry.db")
	g, xs := readFixtureAt(t, path)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	out, err := g.ExecuteRead(context.Background(), xs[0], "scope", func(ctx context.Context) (runtime.ToolResult, error) {
		// A fixture-only timestamp edit simulates a process pause past expiry;
		// the short callback finishes before the renewal ticker can run.
		result, err := db.ExecContext(ctx, "UPDATE resource_leases SET expires=? WHERE scope=? AND released=0", time.Now().Add(-time.Second).UnixNano(), "scope")
		if err != nil {
			return runtime.ToolResult{}, err
		}
		if n, err := result.RowsAffected(); err != nil || n != 1 {
			return runtime.ToolResult{}, errors.New("fixture missing reader")
		}
		if _, err := g.Store.AcquireLease(ctx, xs[2].TaskID, "writer", "scope", true, time.Now().UTC(), 30*time.Second); !errors.Is(err, telemetry.ErrLeaseBusy) {
			t.Error("expired but still-running reader did not exclude writer")
		}
		return runtime.ToolResult{Content: "stale read must not escape", Effect: runtime.NoEffect}, nil
	})
	if !errors.Is(err, tools.ErrExecution) || out.Effect != runtime.UncertainEffect || out.Content != "" {
		t.Fatal("expired reader result accepted", err)
	}
	leases, err := g.Store.InspectLeases(context.Background(), "scope")
	if err != nil || len(leases) != 0 {
		t.Fatal("expired reader not released after return", err)
	}
}

func TestReadGateOverlapsReadersAndBlocksWriter(t *testing.T) {
	g, xs := readFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := make(chan struct{}, 2)
	finish := []chan struct{}{make(chan struct{}), make(chan struct{})}
	done := []chan error{make(chan error, 1), make(chan error, 1)}
	for i := range 2 {
		go func() {
			_, err := g.ExecuteRead(ctx, xs[i], "scope", func(ctx context.Context) (runtime.ToolResult, error) {
				started <- struct{}{}
				select {
				case <-finish[i]:
				case <-ctx.Done():
					return runtime.ToolResult{}, ctx.Err()
				}
				return runtime.ToolResult{Effect: runtime.NoEffect}, nil
			})
			done[i] <- err
		}()
	}
	for range 2 {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("readers did not overlap")
		}
	}
	leases, err := g.Store.InspectLeases(ctx, "scope")
	if err != nil || len(leases) != 2 || leases[0].Writer || leases[1].Writer {
		t.Fatal("shared readers not held", err)
	}
	tryWriter := func() error {
		_, err := g.Store.AcquireLease(ctx, xs[2].TaskID, "writer", "scope", true, time.Now().UTC(), 30*time.Second)
		return err
	}
	if !errors.Is(tryWriter(), telemetry.ErrLeaseBusy) {
		t.Fatal("writer overlapped readers")
	}
	close(finish[0])
	if err := <-done[0]; err != nil {
		t.Fatal(err)
	}
	if !errors.Is(tryWriter(), telemetry.ErrLeaseBusy) {
		t.Fatal("writer overlapped remaining reader")
	}
	close(finish[1])
	if err := <-done[1]; err != nil {
		t.Fatal(err)
	}
	if err := tryWriter(); err != nil {
		t.Fatal("writer unavailable after readers stopped", err)
	}
	called := false
	out, err := g.ExecuteRead(ctx, xs[0], "scope", func(context.Context) (runtime.ToolResult, error) {
		called = true
		return runtime.ToolResult{Effect: runtime.NoEffect}, nil
	})
	if !errors.Is(err, tools.ErrDenied) || called || out.Effect != runtime.NoEffect {
		t.Fatal("reader ignored writer")
	}
}

func TestReadGateCancellationRetainsLeaseUntilReturn(t *testing.T) {
	g, xs := readFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	deadline, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	started, stopping, finish, done := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		out, err := g.ExecuteRead(ctx, xs[0], "scope", func(ctx context.Context) (runtime.ToolResult, error) {
			close(started)
			<-ctx.Done()
			close(stopping)
			select {
			case <-finish:
			case <-deadline.Done():
			}
			return runtime.ToolResult{Content: "must discard", Effect: runtime.NoEffect}, nil
		})
		if !errors.Is(err, tools.ErrExecution) || out.Effect != runtime.UncertainEffect || out.Content != "" {
			t.Error("canceled read accepted")
		}
		done <- err
	}()
	select {
	case <-started:
	case <-deadline.Done():
		t.Fatal("read not started")
	}
	cancel()
	select {
	case <-stopping:
	case <-deadline.Done():
		t.Fatal("read not canceled")
	}
	if _, err := g.Store.AcquireLease(deadline, xs[2].TaskID, "writer", "scope", true, time.Now().UTC(), 30*time.Second); !errors.Is(err, telemetry.ErrLeaseBusy) {
		t.Fatal("writer admitted before canceled reader returned", err)
	}
	select {
	case <-done:
		t.Fatal("callback abandoned")
	default:
	}
	close(finish)
	select {
	case <-done:
	case <-deadline.Done():
		t.Fatal("gate did not join")
	}
	leases, err := g.Store.InspectLeases(deadline, "scope")
	if err != nil || len(leases) != 0 {
		t.Fatal("reader not released", err)
	}
}

func TestReadGateRejectsIdentityAndInvalidOutcomes(t *testing.T) {
	for _, mode := range []string{"session", "attempt", "call", "name", "scope", "error", "panic", "confirmed", "uncertain", "malformed_flags"} {
		t.Run(mode, func(t *testing.T) {
			g, xs := readFixture(t)
			x, scope := xs[0], "scope"
			switch mode {
			case "session":
				x.SessionID = "other"
			case "attempt":
				x.AttemptID = "other"
			case "call":
				x.Call.ID = "other"
			case "name":
				x.Call.Name = "other"
			case "scope":
				scope = "*"
			}
			called := false
			out, err := g.ExecuteRead(context.Background(), x, scope, func(context.Context) (runtime.ToolResult, error) {
				called = true
				switch mode {
				case "panic":
					panic("private")
				case "error":
					return runtime.ToolResult{}, errors.New("private")
				case "confirmed":
					return runtime.ToolResult{Content: "private", Effect: runtime.ConfirmedEffect}, nil
				case "uncertain":
					return runtime.ToolResult{Content: "private", Effect: runtime.UncertainEffect}, nil
				default:
					return runtime.ToolResult{Content: "private", Effect: runtime.NoEffect, Recoverable: true}, nil
				}
			})
			if err == nil || out.Content != "" {
				t.Fatal("invalid read accepted")
			}
			if called != (mode == "error" || mode == "panic" || mode == "confirmed" || mode == "uncertain" || mode == "malformed_flags") {
				t.Fatal("incorrect dispatch")
			}
			leases, err := g.Store.InspectLeases(context.Background(), "scope")
			if err != nil || len(leases) != 0 {
				t.Fatal("leaked reader", err)
			}
		})
	}
}

func TestReadGateKnownFailureCancellationRequiresOwnedLease(t *testing.T) {
	for _, mode := range []string{"caller", "durable_and_caller", "durable_only", "expired", "handler_error", "nonfailed"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "cancellation.db")
			g, xs := readFixtureAt(t, path)
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			out, err := g.ExecuteRead(ctx, xs[0], "scope", func(work context.Context) (runtime.ToolResult, error) {
				if mode == "expired" {
					if _, err := db.ExecContext(work, "UPDATE resource_leases SET expires=? WHERE scope=? AND released=0", time.Now().Add(-time.Second).UnixNano(), "scope"); err != nil {
						return runtime.ToolResult{}, err
					}
				}
				if mode == "durable_and_caller" || mode == "durable_only" {
					if _, err := g.Store.RequestCancellation(work, xs[0].TaskID); err != nil {
						return runtime.ToolResult{}, err
					}
				}
				if mode != "durable_only" {
					cancel()
				}
				result := runtime.ToolResult{Content: "bounded failure evidence", Effect: runtime.NoEffect, Failed: mode != "nonfailed", Recoverable: mode != "nonfailed"}
				if mode == "handler_error" {
					return result, errors.New("private")
				}
				return result, nil
			})
			if mode == "caller" || mode == "durable_and_caller" {
				if !errors.Is(err, context.Canceled) || out.Content != "bounded failure evidence" || out.Effect != runtime.NoEffect || !out.Failed || out.Recoverable {
					t.Fatal("owned cancellation diagnostic lost", out, err)
				}
			} else if !errors.Is(err, tools.ErrExecution) || out.Content != "" || out.Effect != runtime.UncertainEffect {
				t.Fatal("unsafe diagnostic retained", out, err)
			}
			leases, err := g.Store.InspectLeases(context.Background(), "scope")
			if err != nil || len(leases) != 0 {
				t.Fatal("joined canceled reader lease retained", err)
			}
		})
	}
}

func TestReadRenewalCancellationCannotMaskStorageFailure(t *testing.T) {
	for _, caller := range []error{nil, context.Canceled, context.DeadlineExceeded} {
		for _, durable := range []bool{false, true} {
			if err := readRenewalFailure(errors.New("storage failed"), caller, durable); !errors.Is(err, tools.ErrExecution) {
				t.Fatal("storage failure downgraded by cancellation", err)
			}
		}
	}
	for _, storage := range []error{nil, context.Canceled, context.DeadlineExceeded} {
		if err := readRenewalFailure(storage, context.Canceled, false); !errors.Is(err, context.Canceled) {
			t.Fatal("observed cancellation lost", err)
		}
	}
	if err := readRenewalFailure(context.DeadlineExceeded, nil, false); !errors.Is(err, tools.ErrExecution) {
		t.Fatal("heartbeat-only timeout treated as caller cancellation")
	}
	if err := readRenewalFailure(nil, nil, true); !errors.Is(err, context.Canceled) {
		t.Fatal("durable cancellation lost")
	}
}
