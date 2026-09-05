package app

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"time"

	"darwinrouter/internal/telemetry"
	"darwinrouter/runtime"
	"darwinrouter/sessions"
)

var ErrCancellationControl = errors.New("task cancellation control unavailable")

// stop joins the bounded reader before its owning database can close.
func watchCancellation(ctx context.Context, read func(context.Context) (bool, error), cancelRun context.CancelFunc) func() error {
	watchCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			query, cancel := context.WithTimeout(watchCtx, time.Second)
			requested, err := read(query)
			cancel()
			if watchCtx.Err() != nil {
				done <- nil
				return
			}
			if err != nil || requested {
				cancelRun()
				if err != nil {
					done <- ErrCancellationControl
				} else {
					done <- nil
				}
				return
			}
			select {
			case <-watchCtx.Done():
				done <- nil
				return
			case <-ticker.C:
			}
		}
	}()
	return func() error { stop(); return <-done }
}

func (s *Service) CancellationStatus(ctx context.Context, task string) (runtime.CancellationStatus, error) {
	if !sessions.ValidEventPageID(task) {
		return runtime.CancellationStatus{}, ErrAdmission
	}
	db, err := telemetry.OpenReadOnly(ctx, s.settings.Telemetry.Database)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return runtime.CancellationStatus{}, sql.ErrNoRows
		}
		return runtime.CancellationStatus{}, ErrCancellationControl
	}
	defer db.Close()
	status, err := db.CancellationStatus(ctx, task)
	return status, cancellationError(err)
}

func (s *Service) CancelTask(ctx context.Context, task string) (runtime.CancellationStatus, error) {
	// Confirm existence read-only before opening a writer (which may migrate).
	if _, err := s.CancellationStatus(ctx, task); err != nil {
		return runtime.CancellationStatus{}, err
	}
	db, err := telemetry.Open(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return runtime.CancellationStatus{}, ErrCancellationControl
	}
	defer db.Close()
	status, err := db.RequestCancellation(ctx, task)
	return status, cancellationError(err)
}

func cancellationError(err error) error {
	for _, known := range []error{sql.ErrNoRows, telemetry.ErrConflict, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, known) {
			return known
		}
	}
	if err != nil {
		return ErrCancellationControl
	}
	return nil
}
