package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"modernc.org/sqlite"
)

func TestBrowserOperationReceiptCompetesWithRuntimeWrites(t *testing.T) {
	for _, phase := range []string{"begin", "commit", "reject", "recover"} {
		t.Run(phase, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "state.db")
			store, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			peer, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer peer.Close()
			peer.SetMaxOpenConns(1)
			if _, err = peer.Exec(`PRAGMA busy_timeout=5000; CREATE TABLE receipt_runtime_peer(value INTEGER); INSERT INTO receipt_runtime_peer VALUES(0)`); err != nil {
				t.Fatal(err)
			}
			const count = 32
			records := make([]BrowserOperation, count)
			if phase != "begin" {
				for i := range records {
					records[i], err = store.BeginBrowserOperation(ctx, browserTestSubject, fmt.Sprintf("concurrent-browser-key-%02d", i), "approval.deny", []byte(`{"action":"deny"}`))
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			start, stop, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
			go func() {
				<-start
				for {
					select {
					case <-stop:
						done <- nil
						return
					default:
					}
					if _, err := peer.Exec(`UPDATE receipt_runtime_peer SET value=value+1`); err != nil {
						done <- err
						return
					}
					// Leave admission opportunities rather than creating an
					// unbounded writer that can starve every other connection.
					time.Sleep(time.Millisecond)
				}
			}()
			close(start)
			var failure error
			for i, record := range records {
				var receipt BrowserOperation
				wantState := "committed"
				switch phase {
				case "begin":
					wantState = "pending"
					receipt, err = store.BeginBrowserOperation(ctx, browserTestSubject, fmt.Sprintf("concurrent-browser-key-%02d", i), "approval.deny", []byte(`{"action":"deny"}`))
				case "commit":
					receipt, err = store.CommitBrowserOperation(ctx, browserTestSubject, record.OperationID, record.RequestDigest, []byte(`{"state":"denied"}`))
				case "reject":
					wantState = "rejected"
					receipt, err = store.RejectBrowserOperation(ctx, browserTestSubject, record.OperationID, record.RequestDigest, []byte(`{"state":"rejected"}`))
				case "recover":
					receipt, err = store.RecoverBrowserOperation(ctx, "1123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", record, "committed", []byte(`{"state":"denied"}`))
				}
				if err != nil {
					failure = err
					break
				}
				if receipt.State != wantState {
					failure = errors.New("incorrect receipt state")
					break
				}
			}
			close(stop)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if failure != nil {
				t.Fatalf("browser operation %s failed during unrelated writes: %v", phase, failure)
			}
		})
	}
}

func TestBrowserOperationReadBeforeWriteReproducesSnapshotConflict(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record, err := store.BeginBrowserOperation(ctx, browserTestSubject, "snapshot-receipt-key", "approval.deny", []byte(`{"action":"deny"}`))
	if err != nil {
		t.Fatal(err)
	}
	peer, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = readBrowserOperation(ctx, tx, record.OperationID); err != nil {
		t.Fatal(err)
	}
	if _, err = peer.Exec(`UPDATE browser_operations SET updated_at=updated_at WHERE operation_id=?`, record.OperationID); err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(`UPDATE browser_operations SET state='committed',response='{}' WHERE operation_id=?`, record.OperationID)
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) || sqliteErr.Code() != 517 {
		t.Fatalf("expected original deferred read/write ordering to produce SQLITE_BUSY_SNAPSHOT(517), got %v", err)
	}
}

func TestBrowserOperationWriterReservationPrecedesSnapshot(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record, err := store.BeginBrowserOperation(ctx, browserTestSubject, "reservation-receipt-key", "approval.deny", []byte(`{"action":"deny"}`))
	if err != nil {
		t.Fatal(err)
	}
	peer, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	peer.SetMaxOpenConns(1)
	if _, err = peer.Exec(`PRAGMA busy_timeout=0`); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = reserveBrowserOperationWrite(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if _, err = readBrowserOperation(ctx, tx, record.OperationID); err != nil {
		t.Fatal(err)
	}
	_, err = peer.Exec(`UPDATE browser_operations SET updated_at=updated_at WHERE operation_id=?`, record.OperationID)
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) || sqliteErr.Code() != 5 {
		t.Fatalf("writer reservation not held before read: %v", err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err = peer.Exec(`UPDATE browser_operations SET updated_at=updated_at WHERE operation_id=?`, record.OperationID); err != nil {
		t.Fatal("reservation not released", err)
	}
	after, err := readBrowserOperation(ctx, store.db, record.OperationID)
	if err != nil || after.State != "pending" || len(after.Response) != 0 || !after.UpdatedAt.Equal(record.UpdatedAt) {
		t.Fatal("reservation mutated pending operation", after, err)
	}
}
