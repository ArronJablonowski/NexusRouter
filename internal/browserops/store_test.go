package browserops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

const testSubject = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
const otherSubject = "1123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestRequestBindingReceiptReplayAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "browser.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Begin(ctx, testSubject, "browser-operation-key-01", "chat.submit", []byte(`{"text":"hello"}`))
	if err != nil || first.State != "pending" {
		t.Fatal(first, err)
	}
	if _, err = store.Begin(ctx, testSubject, "browser-operation-key-01", "chat.submit", []byte(`{"text":"different"}`)); !errors.Is(err, ErrConflict) {
		t.Fatal("key reuse was not rejected", err)
	}
	committed, err := store.Commit(ctx, testSubject, first.OperationID, first.RequestDigest, []byte(`{"version":1}`))
	if err != nil || committed.State != "committed" {
		t.Fatal(committed, err)
	}
	store.Close()
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	replayed, err := store.Begin(ctx, testSubject, "browser-operation-key-01", "chat.submit", []byte(`{"text":"hello"}`))
	if err != nil || replayed.State != "committed" || string(replayed.Response) != `{"version":1}` {
		t.Fatal(replayed, err)
	}
}

func TestMainDatabaseBackupRestoreIncludesOperationReceipt(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path, restored := filepath.Join(dir, "state.db"), filepath.Join(dir, "restored.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Begin(ctx, testSubject, "browser-operation-key-05", "submit", []byte(`{"text":"backup"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Commit(ctx, testSubject, record.OperationID, record.RequestDigest, []byte(`{"version":1}`)); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(restored, body, 0600); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, restored)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	items, _, err := store.List(ctx, testSubject, "", 10)
	if err != nil || len(items) != 1 || items[0].State != "committed" {
		t.Fatal(items, err)
	}
}

func TestSessionSubjectIsolationAndBoundedRead(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "browser.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first, err := store.Begin(ctx, testSubject, "browser-operation-key-04", "submit", []byte(`{"text":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Begin(ctx, otherSubject, "browser-operation-key-04", "submit", []byte(`{"text":"hello"}`)); err != nil {
		t.Fatal(err)
	}
	items, next, err := store.List(ctx, testSubject, "", 1)
	if err != nil || next != "" || len(items) != 1 || items[0].OperationID != first.OperationID {
		t.Fatal(items, next, err)
	}
	foreign, _, err := store.List(ctx, otherSubject, "", 1)
	if err != nil || len(foreign) != 1 || foreign[0].OperationID == first.OperationID {
		t.Fatal(foreign, err)
	}
}

func TestConcurrentExactBeginConverges(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "browser.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var wait sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			record, err := store.Begin(ctx, testSubject, "browser-operation-key-02", "chat.cancel", []byte(`{"task":"task"}`))
			if err == nil && (!record.Valid() || record.State != "pending") {
				err = ErrInvalid
			}
			errs <- err
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestPendingOperationSurvivesAmbiguousDomainAcknowledgement(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "browser.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Begin(ctx, testSubject, "browser-operation-key-03", "feedback.record", []byte(`{"task":"task"}`))
	if err != nil {
		t.Fatal(err)
	}
	store.Close() // Simulate a crash after the domain commit but before receipt commit.
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	pending, err := store.Begin(ctx, testSubject, "browser-operation-key-03", "feedback.record", []byte(`{"task":"task"}`))
	if err != nil || pending.State != "pending" || pending.OperationID != first.OperationID {
		t.Fatal(pending, err)
	}
	committed, err := store.Commit(ctx, testSubject, pending.OperationID, pending.RequestDigest, []byte(`{"state":"recorded"}`))
	if err != nil || committed.State != "committed" {
		t.Fatal(committed, err)
	}
}

func TestRejectedOperationReplaysAndChangedRequestConflicts(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "browser.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	request := []byte(`{"task":"missing"}`)
	record, err := store.Begin(ctx, testSubject, "browser-operation-key-06", "cancel", request)
	if err != nil {
		t.Fatal(err)
	}
	rejected, err := store.Reject(ctx, testSubject, record.OperationID, record.RequestDigest, []byte(`{"version":1,"code":"not_found"}`))
	if err != nil || rejected.State != "rejected" {
		t.Fatal(rejected, err)
	}
	replayed, err := store.Begin(ctx, testSubject, "browser-operation-key-06", "cancel", request)
	if err != nil || replayed.State != "rejected" || string(replayed.Response) != string(rejected.Response) {
		t.Fatal(replayed, err)
	}
	if _, err = store.Begin(ctx, testSubject, "browser-operation-key-06", "cancel", []byte(`{"task":"other"}`)); !errors.Is(err, ErrConflict) {
		t.Fatal("changed rejected request accepted", err)
	}
}

func TestPendingAdoptionFailsClosedWhenInitiatorIsAmbiguous(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "browser.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	request := []byte(`{"version":1,"idempotency_key":"same-workboard-key","action":"board.create"}`)
	if _, err = store.Begin(ctx, testSubject, "same-workboard-key", "board.create", request); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Begin(ctx, otherSubject, "same-workboard-key", "board.create", request); err != nil {
		t.Fatal(err)
	}
	recoverySubject := "2123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if _, _, err = store.Adoptable(ctx, recoverySubject, "same-workboard-key", "board.create", request); !errors.Is(err, ErrConflict) {
		t.Fatal("ambiguous initiating session was selected", err)
	}
	first, _, err := store.List(ctx, testSubject, "", 10)
	if err != nil || len(first) != 1 || first[0].State != "pending" {
		t.Fatal("failed adoption changed initiator", first, err)
	}
}
