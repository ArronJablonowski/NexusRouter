package workers_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/workers"
)

type untouchedSlotAdapters struct {
	leaseCalls   atomic.Int32
	journalCalls atomic.Int32
}

func (s *untouchedSlotAdapters) AcquireLease(context.Context, string, string, string, bool, time.Time, time.Duration) (workers.Lease, error) {
	s.leaseCalls.Add(1)
	return workers.Lease{}, errors.New("slot execution must not acquire a lease")
}

func (s *untouchedSlotAdapters) RenewLease(context.Context, string, string, time.Time, time.Duration) error {
	s.leaseCalls.Add(1)
	return errors.New("slot execution must not renew a lease")
}

func (s *untouchedSlotAdapters) ReleaseLease(context.Context, string, string) error {
	s.leaseCalls.Add(1)
	return errors.New("slot execution must not release a lease")
}

func (s *untouchedSlotAdapters) Append(context.Context, int64, runtime.Event) error {
	s.journalCalls.Add(1)
	return errors.New("slot execution must not append an event")
}

func newSlotSupervisor(t *testing.T, limit int) (*workers.Supervisor, *untouchedSlotAdapters) {
	t.Helper()
	adapters := &untouchedSlotAdapters{}
	supervisor, err := workers.New(limit, time.Millisecond, time.Second, adapters, adapters)
	if err != nil {
		t.Fatal(err)
	}
	return supervisor, adapters
}

func assertSlotAdaptersUntouched(t *testing.T, adapters *untouchedSlotAdapters) {
	t.Helper()
	if leases, events := adapters.leaseCalls.Load(), adapters.journalCalls.Load(); leases != 0 || events != 0 {
		t.Fatalf("slot-only callback crossed a durable execution boundary: lease calls=%d journal calls=%d", leases, events)
	}
}

func TestWithSlotBoundsCallbacksAndUsesNoDurableAdapters(t *testing.T) {
	supervisor, adapters := newSlotSupervisor(t, 2)
	const callbackCount = 8
	entered := make(chan struct{}, callbackCount)
	release := make(chan struct{})
	var active atomic.Int32
	var peak atomic.Int32
	var wg sync.WaitGroup
	errs := make(chan error, callbackCount)

	for range callbackCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- supervisor.WithSlot(context.Background(), func(context.Context) error {
				current := active.Add(1)
				for observed := peak.Load(); current > observed && !peak.CompareAndSwap(observed, current); observed = peak.Load() {
				}
				entered <- struct{}{}
				<-release
				active.Add(-1)
				return nil
			})
		}()
	}

	for range 2 {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("bounded callbacks did not enter")
		}
	}
	select {
	case <-entered:
		t.Fatal("more callbacks entered than the supervisor limit")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := peak.Load(); got != 2 {
		t.Fatalf("peak concurrency = %d, want 2", got)
	}
	assertSlotAdaptersUntouched(t, adapters)
}

func TestWithSlotCancellationWhileWaitingDoesNotRunCallback(t *testing.T) {
	supervisor, adapters := newSlotSupervisor(t, 1)
	occupied, release := make(chan struct{}), make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- supervisor.WithSlot(context.Background(), func(context.Context) error {
			close(occupied)
			<-release
			return nil
		})
	}()
	<-occupied

	ctx, cancel := context.WithCancel(context.Background())
	var called atomic.Bool
	waiterDone := make(chan error, 1)
	go func() {
		waiterDone <- supervisor.WithSlot(ctx, func(context.Context) error {
			called.Store(true)
			return nil
		})
	}()
	cancel()
	if err := <-waiterDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting cancellation = %v, want context cancellation", err)
	}
	if called.Load() {
		t.Fatal("callback ran after its slot wait was canceled")
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	assertSlotAdaptersUntouched(t, adapters)
}

func TestWithSlotJoinsCanceledCallbackBeforeReusingSlot(t *testing.T) {
	supervisor, adapters := newSlotSupervisor(t, 1)
	ctx, cancel := context.WithCancel(context.Background())
	started, sawCancel, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- supervisor.WithSlot(ctx, func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			close(sawCancel)
			<-release
			return ctx.Err()
		})
	}()
	<-started
	cancel()
	<-sawCancel

	secondCtx, secondCancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer secondCancel()
	var secondCalled atomic.Bool
	if err := supervisor.WithSlot(secondCtx, func(context.Context) error {
		secondCalled.Store(true)
		return nil
	}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second slot wait = %v, want deadline exceeded", err)
	}
	if secondCalled.Load() {
		t.Fatal("slot was reused before the canceled callback joined")
	}
	select {
	case err := <-firstDone:
		t.Fatalf("WithSlot returned before canceled callback joined: %v", err)
	default:
	}
	close(release)
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("joined callback = %v, want context cancellation", err)
	}
	if err := supervisor.WithSlot(context.Background(), func(context.Context) error {
		secondCalled.Store(true)
		return nil
	}); err != nil || !secondCalled.Load() {
		t.Fatalf("joined slot was not reusable: called=%v err=%v", secondCalled.Load(), err)
	}
	assertSlotAdaptersUntouched(t, adapters)
}

func TestWithSlotContainsPanicAndReleasesSlot(t *testing.T) {
	supervisor, adapters := newSlotSupervisor(t, 1)
	if err := supervisor.WithSlot(context.Background(), func(context.Context) error {
		panic("private callback payload")
	}); err != workers.ErrWork {
		t.Fatalf("panic result = %v, want sanitized ErrWork", err)
	}
	called := false
	if err := supervisor.WithSlot(context.Background(), func(context.Context) error {
		called = true
		return nil
	}); err != nil || !called {
		t.Fatalf("panic retained slot: called=%v err=%v", called, err)
	}
	assertSlotAdaptersUntouched(t, adapters)
}

func TestWithSlotPreservesCallbackErrorAndRejectsInvalidInput(t *testing.T) {
	supervisor, adapters := newSlotSupervisor(t, 1)
	want := errors.New("callback failed")
	if err := supervisor.WithSlot(context.Background(), func(context.Context) error { return want }); !errors.Is(err, want) {
		t.Fatalf("callback error = %v, want %v", err, want)
	}
	if err := supervisor.WithSlot(context.Background(), nil); err != workers.ErrWork {
		t.Fatalf("nil callback = %v, want ErrWork", err)
	}
	var nilSupervisor *workers.Supervisor
	if err := nilSupervisor.WithSlot(context.Background(), func(context.Context) error { return nil }); err != workers.ErrWork {
		t.Fatalf("nil supervisor = %v, want ErrWork", err)
	}
	assertSlotAdaptersUntouched(t, adapters)
}
