package app

import (
	"context"
	"reflect"
	"sync"
	"time"

	"github.com/ArronJablonowski/NexusRouter/health"
)

type providerHealthWriter interface {
	RecordProviderHealth(context.Context, health.Report, int) error
}

// ProviderHealthRecorder samples sequentially and waits one full interval after
// each attempt. Slow probes therefore cannot overlap or accumulate ticker debt.
// Failures are supplemental and do not stop task serving; a later sample can
// recover without replaying the failed attempt.
type ProviderHealthRecorder struct {
	mu          sync.Mutex
	closeOnce   sync.Once
	cancel      context.CancelFunc
	done        chan struct{}
	lastAttempt time.Time
	lastSuccess time.Time
	lastErr     error
}

func StartProviderHealthRecorder(ctx context.Context, writer providerHealthWriter,
	report func(context.Context) (health.Report, error), interval time.Duration, retain int,
) (*ProviderHealthRecorder, error) {
	if ctx == nil || ctx.Err() != nil || nilProviderHealthWriter(writer) || report == nil || interval < 5*time.Second || interval > time.Hour || retain < 1 || retain > 100000 {
		return nil, ErrHealth
	}
	ctx, cancel := context.WithCancel(ctx)
	recorder := &ProviderHealthRecorder{cancel: cancel, done: make(chan struct{})}
	go recorder.run(ctx, writer, report, interval, retain)
	return recorder, nil
}

func nilProviderHealthWriter(writer providerHealthWriter) bool {
	if writer == nil {
		return true
	}
	value := reflect.ValueOf(writer)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	}
	return false
}

func (r *ProviderHealthRecorder) run(ctx context.Context, writer providerHealthWriter,
	report func(context.Context) (health.Report, error), interval time.Duration, retain int,
) {
	defer close(r.done)
	defer func() {
		if recover() != nil {
			r.mu.Lock()
			r.lastErr = ErrHealth
			r.mu.Unlock()
		}
	}()
	for ctx.Err() == nil {
		now := time.Now().UTC()
		r.mu.Lock()
		r.lastAttempt = now
		r.mu.Unlock()
		sample, err := report(ctx)
		if err == nil && ctx.Err() == nil {
			err = writer.RecordProviderHealth(ctx, sample, retain)
		}
		if ctx.Err() != nil {
			return
		}
		r.mu.Lock()
		r.lastErr = err
		if err == nil {
			r.lastSuccess = now
		}
		r.mu.Unlock()
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (r *ProviderHealthRecorder) Close() {
	if r == nil || r.done == nil {
		return
	}
	r.closeOnce.Do(func() {
		if r.cancel != nil {
			r.cancel()
		}
		<-r.done
	})
}

func (r *ProviderHealthRecorder) Snapshot() (attempt, success time.Time, err error) {
	if r == nil || r.done == nil {
		return time.Time{}, time.Time{}, ErrHealth
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastAttempt, r.lastSuccess, r.lastErr
}
