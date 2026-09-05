package app

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"darwinrouter/internal/config"
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
		}, cancelRun)
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
