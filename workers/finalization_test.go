package workers_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/workers"
)

// Explicit forwarding avoids accidentally inheriting the optional finalizer.
type legacyWorkerJournal struct{ runtime.Journal }

func (j legacyWorkerJournal) AppendLeased(ctx context.Context, seq int64, e runtime.Event, token, owner string) error {
	return j.Journal.(workers.LeaseJournal).AppendLeased(ctx, seq, e, token, owner)
}

type finalWorkerJournal struct {
	legacyWorkerJournal
	finish func(context.Context, int64, runtime.Event, string, string) error
}

func (j finalWorkerJournal) FinishLeased(ctx context.Context, seq int64, e runtime.Event, token, owner string) error {
	return j.finish(ctx, seq, e, token, owner)
}

type releaseObserved struct {
	workers.LeaseStore
	calls        atomic.Int32
	fail, panics bool
}

type panicRenewLease struct{ *releaseObserved }

func (s panicRenewLease) RenewLease(context.Context, string, string, time.Time, time.Duration) error {
	panic("private renewal panic")
}

type panicHeartbeatJournal struct{ legacyWorkerJournal }

func (j panicHeartbeatJournal) AppendLeased(ctx context.Context, seq int64, e runtime.Event, token, owner string) error {
	if e.Kind == runtime.WorkerHeartbeat {
		panic("private heartbeat panic")
	}
	return j.legacyWorkerJournal.AppendLeased(ctx, seq, e, token, owner)
}

func (s *releaseObserved) ReleaseLease(ctx context.Context, token, owner string) error {
	s.calls.Add(1)
	if s.panics {
		panic("private release failure")
	}
	if s.fail {
		return errors.New("private release failure")
	}
	return s.LeaseStore.ReleaseLease(ctx, token, owner)
}

func TestWorkerFinalizationWaitsForExecuteAndValidateJoin(t *testing.T) {
	s, _ := setup(t)
	finalizer, ok := any(s).(workers.FinalizingLeaseJournal)
	if !ok {
		t.Fatal("production store lacks atomic worker finalization")
	}
	store := &releaseObserved{LeaseStore: s}
	var executeJoined, validateJoined atomic.Bool
	var finalized atomic.Int32
	j := finalWorkerJournal{legacyWorkerJournal: legacyWorkerJournal{s}, finish: func(ctx context.Context, seq int64, e runtime.Event, token, owner string) error {
		if !executeJoined.Load() || !validateJoined.Load() {
			t.Error("finalization before callback join")
		}
		finalized.Add(1)
		return finalizer.FinishLeased(ctx, seq, e, token, owner)
	}}
	sup, err := workers.New(1, time.Second, 5*time.Second, store, j)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	validateEntered, releaseValidate := make(chan struct{}), make(chan struct{})
	w := work("atomic")
	w.Execute = func(context.Context) (string, error) { defer executeJoined.Store(true); return "accepted", nil }
	w.Validate = func(ctx context.Context, _ string) error {
		defer validateJoined.Store(true)
		close(validateEntered)
		select {
		case <-releaseValidate:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	type result struct {
		text string
		err  error
	}
	done := make(chan result, 1)
	go func() { text, err := sup.Run(ctx, w); done <- result{text, err} }()
	select {
	case <-validateEntered:
	case <-ctx.Done():
		t.Fatal("validation never entered")
	}
	if finalized.Load() != 0 || store.calls.Load() != 0 {
		t.Fatal("released before validator returned")
	}
	leases, err := s.InspectLeases(ctx, w.Scope)
	if err != nil || len(leases) != 1 {
		t.Fatal("ownership lost during validation", err)
	}
	close(releaseValidate)
	select {
	case got := <-done:
		if got.err != nil || got.text != "accepted" {
			t.Fatal(got)
		}
	case <-ctx.Done():
		t.Fatal("worker did not join")
	}
	if finalized.Load() != 1 || store.calls.Load() != 0 {
		t.Fatal("finalization acknowledgement duplicated release")
	}
	events, err := s.Read(ctx, w.TaskID, 0, 100)
	if err != nil || events[len(events)-1].Kind != runtime.TaskCompleted {
		t.Fatal("missing terminal evidence", err)
	}
	leases, err = s.InspectLeases(ctx, w.Scope)
	if err != nil || len(leases) != 0 {
		t.Fatal("terminal worker retained lease", err)
	}
}

func TestWorkerFinalizationFailureNeverReleasesOutput(t *testing.T) {
	for _, mode := range []string{"error", "panic", "ambiguous_ack", "canceled_error"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := setup(t)
			store := &releaseObserved{LeaseStore: s}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var joined atomic.Bool
			calls := 0
			j := finalWorkerJournal{legacyWorkerJournal: legacyWorkerJournal{s}, finish: func(ctx context.Context, seq int64, e runtime.Event, token, owner string) error {
				calls++
				if !joined.Load() {
					t.Error("finalization before execute join")
				}
				if mode == "panic" {
					panic("private finalize error")
				}
				if mode == "ambiguous_ack" {
					finalizer, ok := any(s).(workers.FinalizingLeaseJournal)
					if !ok {
						return errors.New("missing finalizer")
					}
					if err := finalizer.FinishLeased(ctx, seq, e, token, owner); err != nil {
						return err
					}
				}
				return errors.New("private finalize error")
			}}
			sup, err := workers.New(1, time.Second, 5*time.Second, store, j)
			if err != nil {
				t.Fatal(err)
			}
			w := work("failed")
			w.Execute = func(context.Context) (string, error) {
				defer joined.Store(true)
				if mode == "canceled_error" {
					cancel()
				}
				return "must not escape", nil
			}
			out, err := sup.Run(ctx, w)
			if out != "" || !errors.Is(err, workers.ErrDurability) || strings.Contains(err.Error(), "private") || calls != 1 || store.calls.Load() != 1 {
				t.Fatal("false success or leaked error", out, err, calls)
			}
			if mode == "canceled_error" && !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation identity lost", err)
			}
		})
	}
}

func TestLegacyWorkerReleaseFailureSuppressesSuccess(t *testing.T) {
	for _, mode := range []string{"success", "canceled", "panic"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := setup(t)
			store := &releaseObserved{LeaseStore: s, fail: true, panics: mode == "panic"}
			sup, err := workers.New(1, time.Second, 5*time.Second, store, legacyWorkerJournal{s})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			w := work("legacy")
			w.Execute = func(context.Context) (string, error) {
				if mode == "canceled" {
					cancel()
				}
				return "accepted", nil
			}
			out, err := sup.Run(ctx, w)
			if out != "" || !errors.Is(err, workers.ErrDurability) || store.calls.Load() != 1 || strings.Contains(err.Error(), "private") {
				t.Fatal("release failure ignored", out, err)
			}
			if mode == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation lost", err)
			}
		})
	}
}

func TestWorkerAdapterPanicJoinsBeforeLeaseAndSlotRelease(t *testing.T) {
	for _, mode := range []string{"renew", "heartbeat"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := setup(t)
			observed := &releaseObserved{LeaseStore: s}
			var lease workers.LeaseStore = observed
			var journal runtime.Journal = legacyWorkerJournal{s}
			if mode == "renew" {
				lease = panicRenewLease{observed}
			} else {
				journal = panicHeartbeatJournal{legacyWorkerJournal{s}}
			}
			sup, err := workers.New(1, time.Millisecond, time.Second, lease, journal)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			cleanup, finish := make(chan struct{}), make(chan struct{})
			w := work("panic")
			w.Execute = func(run context.Context) (string, error) {
				<-run.Done()
				close(cleanup)
				select {
				case <-finish:
				case <-ctx.Done():
				}
				return "must not escape", nil
			}
			done := make(chan error, 1)
			go func() {
				out, err := sup.Run(ctx, w)
				if out != "" {
					t.Error("panic output escaped")
				}
				done <- err
			}()
			select {
			case <-cleanup:
			case <-ctx.Done():
				t.Fatal("panic did not cancel callback")
			}
			if observed.calls.Load() != 0 {
				t.Fatal("released before callback joined")
			}
			leases, err := s.InspectLeases(ctx, w.Scope)
			if err != nil || len(leases) != 1 {
				t.Fatal("panic lost live ownership", err)
			}
			blocked, stop := context.WithTimeout(ctx, 10*time.Millisecond)
			_, err = sup.Run(blocked, work("second"))
			stop()
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("slot released before join", err)
			}
			close(finish)
			select {
			case err := <-done:
				if !errors.Is(err, workers.ErrDurability) || strings.Contains(err.Error(), "private") {
					t.Fatal("panic not sanitized", err)
				}
			case <-ctx.Done():
				t.Fatal("panic cleanup not joined")
			}
			if observed.calls.Load() != 1 {
				t.Fatal("joined lease release missing")
			}
		})
	}
}
