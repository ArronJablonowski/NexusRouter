package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func event(id string, sequence int64, kind runtime.Kind) runtime.Event {
	return runtime.Event{Version: 1, ID: id, TaskID: "task", SessionID: "session", CorrelationID: "correlation", Sequence: sequence, Time: time.Unix(100, 0), Kind: kind}
}

// rewriteCanonicalEventForTest keeps the immutable ledger binding coherent for
// fixtures that intentionally model a different, otherwise legitimate event.
func rewriteCanonicalEventForTest(t *testing.T, store *Store, task string, sequence int64, mutate func(*runtime.Event)) {
	t.Helper()
	var body []byte
	if err := store.db.QueryRow(`SELECT body FROM events WHERE task_id=? AND sequence=?`, task, sequence).Scan(&body); err != nil {
		t.Fatal(err)
	}
	var event runtime.Event
	if err := json.Unmarshal(body, &event); err != nil {
		t.Fatal(err)
	}
	mutate(&event)
	body, err := event.Encode()
	if err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE events SET body=? WHERE task_id=? AND sequence=?`, body, task, sequence); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`UPDATE event_log SET body_digest=? WHERE task_id=? AND task_sequence=?`, streamBodyDigest(body), task, sequence); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`UPDATE submission_stream_events SET body_digest=? WHERE task_id=? AND task_sequence=?`, streamBodyDigest(body), task, sequence); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func insertCanonicalEventForTest(t *testing.T, store *Store, event runtime.Event, state string) {
	t.Helper()
	body, err := event.Encode()
	if err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO events(id,task_id,sequence,body) VALUES(?,?,?,?)`, event.ID, event.TaskID, event.Sequence, body); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`INSERT INTO event_log(event_id,task_id,task_sequence,body_digest) VALUES(?,?,?,?)`, event.ID, event.TaskID, event.Sequence, streamBodyDigest(body)); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`UPDATE task_heads SET sequence=?,state=? WHERE task_id=?`, event.Sequence, state, event.TaskID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenSamePathSerializesInitialization(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.db")
	seed, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err = seed.Close(); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(filepath.Dir(path), "shared-alias.db")
	if err = os.Symlink(path, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	start := make(chan struct{})
	stores := make(chan *Store, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, openPath := range []string{path, alias} {
		wg.Add(1)
		go func(openPath string) {
			defer wg.Done()
			<-start
			store, err := Open(context.Background(), openPath)
			stores <- store
			errs <- err
		}(openPath)
	}
	close(start)
	wg.Wait()
	close(stores)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for store := range stores {
		if store == nil {
			t.Fatal("concurrent open returned a nil store")
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStoreOpenLockHonorsCancellation(t *testing.T) {
	release, err := lockStoreOpenPath(context.Background(), "test-lock")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = lockStoreOpenPath(ctx, "test-lock"); !errors.Is(err, context.Canceled) {
		release()
		t.Fatal("canceled lock wait was admitted", err)
	}
	release()
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

func TestAppendPreservesEventEnvelopeIdentities(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	start := event("start", 1, runtime.TaskStarted)
	start.TaskID = "task-identity"
	start.SessionID = "session-identity"
	start.CausationID = "request-identity"
	start.CorrelationID = "correlation-identity"
	if err := s.Append(ctx, 0, start); err != nil {
		t.Fatal(err)
	}
	turn := event("turn", 2, runtime.TurnStarted)
	turn.TaskID, turn.SessionID = start.TaskID, start.SessionID
	turn.CausationID, turn.CorrelationID = start.ID, start.CorrelationID
	turn.TurnID, turn.AttemptID = "turn-identity", "attempt-identity"
	if err := s.Append(ctx, 1, turn); err != nil {
		t.Fatal(err)
	}
	got, err := s.Read(ctx, start.TaskID, 0, 10)
	if err != nil || len(got) != 2 {
		t.Fatal(got, err)
	}
	for i, want := range []runtime.Event{start, turn} {
		if got[i].TaskID != want.TaskID || got[i].SessionID != want.SessionID || got[i].CausationID != want.CausationID || got[i].CorrelationID != want.CorrelationID {
			t.Fatalf("event envelope identity changed: got=%+v want=%+v", got[i], want)
		}
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
	if _, err = s.db.Exec(fmt.Sprintf("PRAGMA user_version=%d", currentStorageSchema+1)); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if reopened, err := Open(context.Background(), path); err == nil {
		reopened.Close()
		t.Fatal("future schema accepted")
	}
}
