package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"darwinrouter/runtime"
)

func cancellationStore(t *testing.T) (*Store, string, runtime.Event) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cancel.db")
	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	start := event("start", 1, runtime.TaskStarted)
	if err := db.Append(context.Background(), 0, start); err != nil {
		t.Fatal(err)
	}
	return db, path, start
}

func TestCancellationRequestDurableIdempotentAndReadOnly(t *testing.T) {
	ctx := context.Background()
	db, path, _ := cancellationStore(t)
	first, err := db.RequestCancellation(ctx, "task")
	if err != nil || !first.Requested || first.RequestID == "" || first.RequestedAt == nil || first.State != "running" {
		t.Fatalf("status=%+v error=%v", first, err)
	}
	again, err := db.RequestCancellation(ctx, "task")
	if err != nil || !reflect.DeepEqual(first, again) {
		t.Fatal("request changed on retry", again, err)
	}
	terminal := event("canceled", 2, runtime.TaskCanceled)
	if err := db.Append(ctx, 1, terminal); err != nil {
		t.Fatal(err)
	}
	after, err := db.RequestCancellation(ctx, "task")
	if err != nil || after.RequestID != first.RequestID || !after.RequestedAt.Equal(*first.RequestedAt) || after.State != "canceled" {
		t.Fatal("terminal retry changed request", after, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	got, err := ro.CancellationStatus(ctx, "task")
	if err != nil || !reflect.DeepEqual(got, after) {
		t.Fatal("restart lost request", got, err)
	}
	if yes, err := ro.CancellationRequested(ctx, "task"); err != nil || !yes {
		t.Fatal(yes, err)
	}
	if _, err := ro.RequestCancellation(ctx, "task"); err == nil {
		t.Fatal("read-only request wrote")
	}
	if _, err := ro.CancellationStatus(ctx, "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("missing status", err)
	}
}

func TestCancellationGateNoEffectAndCleanup(t *testing.T) {
	ctx := context.Background()
	db, _, start := cancellationStore(t)
	if _, err := db.RequestCancellation(ctx, "task"); err != nil {
		t.Fatal(err)
	}
	if err := db.Append(ctx, 0, start); err != nil {
		t.Fatal("exact retry blocked", err)
	}
	for _, kind := range []runtime.Kind{runtime.TaskCompleted, runtime.TaskFailed, runtime.TurnStarted, runtime.RouteSelected, runtime.EvaluationRecorded} {
		e := event("new-"+string(kind), 2, kind)
		e.TurnID = "turn"
		e.RouteID = "route"
		e.Data.ModelID = "model"
		e.Data.ProviderID = "provider"
		yes := true
		e.Data.Accepted = &yes
		if err := db.Append(ctx, 1, e); !errors.Is(err, runtime.ErrCancellationRequested) {
			t.Fatalf("kind=%s error=%v", kind, err)
		}
	}
	var seq, count int
	if err := db.db.QueryRow("SELECT sequence FROM task_heads WHERE task_id='task'").Scan(&seq); err != nil {
		t.Fatal(err)
	}
	if err := db.db.QueryRow("SELECT count(*) FROM events").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if seq != 1 || count != 1 {
		t.Fatal("rejected append had effects", seq, count)
	}
	cleanup := event("tool-cleanup", 2, runtime.ToolCompleted)
	cleanup.TurnID = "turn"
	cleanup.Data = runtime.Data{ToolCallID: "call", ToolName: "read", Effect: runtime.NoEffect}
	if err := db.Append(ctx, 1, cleanup); err != nil {
		t.Fatal("tool cleanup blocked", err)
	}
	if err := db.Append(ctx, 2, event("terminal", 3, runtime.TaskCanceled)); err != nil {
		t.Fatal("cancel terminal blocked", err)
	}
}

func TestCancellationCompletionRaceHasSingleWinner(t *testing.T) {
	for i := 0; i < 8; i++ {
		ctx := context.Background()
		db, _, _ := cancellationStore(t)
		ready := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		var requested runtime.CancellationStatus
		var requestErr, appendErr error
		go func() { defer wg.Done(); <-ready; requested, requestErr = db.RequestCancellation(ctx, "task") }()
		go func() {
			defer wg.Done()
			<-ready
			appendErr = db.Append(ctx, 1, event("complete", 2, runtime.TaskCompleted))
		}()
		close(ready)
		wg.Wait()
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		status, err := db.CancellationStatus(ctx, "task")
		if err != nil {
			t.Fatal(err)
		}
		if appendErr == nil {
			if requested.Requested || status.Requested || status.State != "completed" {
				t.Fatal("completion winner acquired late request", requested, status)
			}
		} else if !errors.Is(appendErr, runtime.ErrCancellationRequested) || !requested.Requested || status.State != "running" {
			t.Fatal("invalid cancellation winner", appendErr, requested, status)
		}
	}
}

func TestCancellationRollbackMissingAndLegacyStatus(t *testing.T) {
	ctx := context.Background()
	db, path, _ := cancellationStore(t)
	if _, err := db.RequestCancellation(ctx, "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("missing request", err)
	}
	if yes, err := db.CancellationRequested(ctx, "missing"); err != nil || yes {
		t.Fatal("missing watcher status", yes, err)
	}
	if _, err := db.db.Exec("CREATE TRIGGER reject_cancellation BEFORE INSERT ON task_cancellations BEGIN SELECT RAISE(ABORT,'fixture'); END"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RequestCancellation(ctx, "task"); err == nil {
		t.Fatal("insert failure ignored")
	}
	if status, err := db.CancellationStatus(ctx, "task"); err != nil || status.Requested || status.State != "running" {
		t.Fatal("failed insert not rolled back", status, err)
	}
	if _, err := db.db.Exec("DROP TRIGGER reject_cancellation; DROP INDEX events_submission_start; DROP TABLE submission_recoveries; DROP TABLE submissions; DROP TABLE task_cancellations; PRAGMA user_version=10"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	status, err := ro.CancellationStatus(ctx, "task")
	if err != nil || status.Requested {
		t.Fatal("legacy read-only status", status, err)
	}
	ro.Close()
	upgraded, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	if status, err := upgraded.RequestCancellation(ctx, "task"); err != nil || !status.Requested {
		t.Fatal("migration failed", status, err)
	}
}

func TestCancellationAfterTerminalDoesNotInsert(t *testing.T) {
	for _, kind := range []runtime.Kind{runtime.TaskCompleted, runtime.TaskFailed, runtime.TaskCanceled} {
		db, _, _ := cancellationStore(t)
		if err := db.Append(context.Background(), 1, event("terminal", 2, kind)); err != nil {
			t.Fatal(err)
		}
		status, err := db.RequestCancellation(context.Background(), "task")
		if err != nil || status.Requested || status.RequestID != "" || status.RequestedAt != nil {
			t.Fatalf("terminal cancellation inserted request: %+v %v", status, err)
		}
		var rows int
		if err := db.db.QueryRow("SELECT count(*) FROM task_cancellations").Scan(&rows); err != nil || rows != 0 {
			t.Fatal("terminal request mutated storage", rows, err)
		}
	}
}
