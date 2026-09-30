package app

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
)

func TestCancellationWatcherFailsClosedAndJoins(t *testing.T) {
	for _, failure := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		run, cancelRun := context.WithCancel(ctx)
		stop := watchCancellation(run, func(context.Context) (bool, error) {
			if failure {
				return false, errors.New("private database detail")
			}
			return true, nil
		}, cancelRun, nil)
		select {
		case <-run.Done():
		case <-ctx.Done():
			t.Fatal("watcher did not cancel")
		}
		err := stop()
		if failure != errors.Is(err, ErrCancellationControl) {
			t.Fatal(err)
		}
		cancelRun()
		cancel()
	}
}

func TestCancellationWatcherDoesNotInterruptDurableBoundary(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	run, cancelRun := context.WithCancel(ctx)
	boundary := &sync.Mutex{}
	boundary.Lock()
	read := make(chan struct{})
	stop := watchCancellation(run, func(context.Context) (bool, error) {
		close(read)
		return true, nil
	}, cancelRun, boundary)
	select {
	case <-read:
	case <-ctx.Done():
		t.Fatal("watcher did not read cancellation")
	}

	select {
	case <-run.Done():
		t.Fatal("cancellation interrupted durable boundary")
	default:
	}
	boundary.Unlock()
	select {
	case <-run.Done():
	case <-ctx.Done():
		t.Fatal("watcher did not cancel after durable boundary")
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
}

func TestCancelMissingDatabaseDoesNotCreateIt(t *testing.T) {
	cfg := config.Defaults()
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "missing.db")
	s := &Service{settings: cfg}
	_, err := s.CancelTask(context.Background(), "missing")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	if _, err := os.Stat(cfg.Telemetry.Database); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}
