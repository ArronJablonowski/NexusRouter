package app

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/resources"
)

var errSwapGrowth = errors.New("Mac swap growth limit exceeded")

// Watch only this execution. Canceling its provider context stops generation without
// killing shared servers or unloading models owned by other work.
func (s *Service) guardMacSwap(ctx context.Context, model config.Model, release func() error) (context.Context, func() error, error) {
	if runtime.GOOS != "darwin" || model.Locality != "local" {
		return ctx, release, nil
	}
	probe, cancel := context.WithTimeout(ctx, 2*time.Second)
	initial, err := s.resourceProfile(probe)
	cancel()
	// Only Darwin measurements represent the Mac swap counter; synthetic or
	// other-platform profiles do not establish a Mac swap baseline.
	if err == nil && initial.Source != "darwin-vm-stat-estimate" {
		return ctx, release, nil
	}
	if err != nil || initial.SwapUsed == nil {
		_ = release()
		return ctx, nil, ErrAdmission
	}
	run, finish := watchSwapGrowth(ctx, *initial.SwapUsed, s.settings.Hardware.MacSwapGrowthBytes(), time.Second, s.resourceProfile)
	var once sync.Once
	var result error
	return run, func() error {
		once.Do(func() {
			guardErr := finish()
			result = release()
			if guardErr != nil {
				result = guardErr
			}
		})
		return result
	}, nil
}

func watchSwapGrowth(ctx context.Context, baseline, limit uint64, interval time.Duration, profile func(context.Context) (resources.Snapshot, error)) (context.Context, func() error) {
	run, cancel := context.WithCancelCause(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-run.Done():
				return
			case <-ticker.C:
				probe, stop := context.WithTimeout(run, 2*time.Second)
				snapshot, err := profile(probe)
				stop()
				if run.Err() != nil {
					return
				}
				if err != nil || snapshot.SwapUsed == nil {
					cancel(ErrAdmission)
					return
				}
				if *snapshot.SwapUsed > baseline && *snapshot.SwapUsed-baseline > limit {
					cancel(errSwapGrowth)
					return
				}
			}
		}
	}()
	return run, func() error {
		cancel(context.Canceled)
		<-done
		cause := context.Cause(run)
		if cause == errSwapGrowth || cause == ErrAdmission {
			return cause
		}
		return nil
	}
}
