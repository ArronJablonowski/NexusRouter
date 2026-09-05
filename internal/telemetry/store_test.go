package telemetry

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"darwinrouter/runtime"
)

func event(id string, sequence int64, kind runtime.Kind) runtime.Event {
	return runtime.Event{Version: 1, ID: id, TaskID: "task", SessionID: "session", CorrelationID: "correlation", Sequence: sequence, Time: time.Unix(100, 0), Kind: kind}
}

func TestRecoveryIdempotencyAndTerminalState(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	start := event("start", 1, runtime.TaskStarted)
	if err = s.Append(ctx, 0, start); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Append(ctx, 0, start); err != nil {
		t.Fatal("ack loss retry:", err)
	}
	changed := start
	changed.Data.Text = "different"
	if err = s.Append(ctx, 0, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("ID reuse:", err)
	}
	if err = s.Append(ctx, 1, event("done", 2, runtime.TaskCompleted)); err != nil {
		t.Fatal(err)
	}
	if err = s.Append(ctx, 2, event("again", 3, runtime.TaskCompleted)); !errors.Is(err, ErrConflict) {
		t.Fatal("terminal task accepted append:", err)
	}
	items, err := s.Read(ctx, "task", 0, 100)
	if err != nil || len(items) != 2 {
		t.Fatalf("replay: %v %v", items, err)
	}
	page, err := s.Read(ctx, "task", 1, 1)
	if err != nil || len(page) != 1 || page[0].ID != "done" {
		t.Fatalf("page: %v %v", page, err)
	}
}

func TestConcurrentWriters(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	a, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if err = a.Append(ctx, 0, event("start", 1, runtime.TaskStarted)); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i, store := range []*Store{a, b} {
		wg.Go(func() { errs <- store.Append(ctx, 1, event(string(rune('a'+i)), 2, runtime.TaskCompleted)) })
	}
	wg.Wait()
	close(errs)
	success, conflicts := 0, 0
	for err := range errs {
		if err == nil {
			success++
		} else if errors.Is(err, ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatalf("success=%d conflicts=%d", success, conflicts)
	}
}

func TestCanceledAppendAndFutureMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = s.Append(ctx, 0, event("start", 1, runtime.TaskStarted)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	items, err := s.Read(context.Background(), "task", 0, 10)
	if err != nil || len(items) != 0 {
		t.Fatal("cancellation persisted state")
	}
	if _, err = s.db.Exec("PRAGMA user_version=13"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if reopened, err := Open(context.Background(), path); err == nil {
		reopened.Close()
		t.Fatal("future schema accepted")
	}
}
