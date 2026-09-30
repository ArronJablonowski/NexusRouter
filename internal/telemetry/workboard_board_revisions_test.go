package telemetry

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/workboard"
)

func TestWorkboardReviseReplayRestartConflictsAndRollback(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 9, 16, 0, 0, 0, time.UTC)
	actor := workboard.Actor{ID: "operator", Type: "operator"}
	created, err := store.CreateWorkboard(ctx, "session", createBoardRequest("create-revise-01", "Original", "old"), actor, now)
	if err != nil {
		t.Fatal(err)
	}
	title, description := "Revised", ""
	request := workboard.ReviseBoardRequest{Version: 1, BoardID: created.BoardID, IdempotencyKey: "revise-operation-1", ExpectedRevision: 1, Title: &title, Description: &description}
	if _, err = store.db.Exec(`CREATE TRIGGER test_fail_revise BEFORE INSERT ON workboard_events WHEN NEW.kind='board.revise' BEGIN SELECT RAISE(ABORT,'injected revise failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReviseWorkboard(ctx, request, actor, now.Add(time.Minute)); err == nil || !strings.Contains(err.Error(), "injected revise failure") {
		t.Fatalf("injected failure = %v", err)
	}
	assertWorkboardCounts(t, store, 1, 1, 1)
	snapshot, err := store.ReadWorkboard(ctx, created.BoardID, workboard.BoardSnapshotOptions{Limit: 1})
	if err != nil || snapshot.Board.Title != "Original" || snapshot.Board.Description != "old" || snapshot.Board.Revision != 1 {
		t.Fatalf("rollback snapshot=%+v err=%v", snapshot.Board, err)
	}
	if _, err = store.db.Exec(`DROP TRIGGER test_fail_revise`); err != nil {
		t.Fatal(err)
	}
	receipt, err := store.ReviseWorkboard(ctx, request, actor, now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if receipt.BoardRevision != 2 || receipt.FirstSequence != 2 || receipt.EventCount != 1 {
		t.Fatalf("receipt=%+v", receipt)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	replayed, err := store.ReviseWorkboard(ctx, request, actor, now.Add(3*time.Minute))
	if err != nil || !reflect.DeepEqual(replayed, receipt) {
		t.Fatalf("restart replay=%+v err=%v", replayed, err)
	}
	changed := request
	changed.ExpectedRevision = 2
	if _, err = store.ReviseWorkboard(ctx, changed, actor, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("same key changed request = %v", err)
	}
	otherTitle := "Other"
	stale := workboard.ReviseBoardRequest{Version: 1, BoardID: created.BoardID, IdempotencyKey: "revise-operation-2", ExpectedRevision: 1, Title: &otherTitle}
	if _, err = store.ReviseWorkboard(ctx, stale, actor, now); !errors.Is(err, &workboard.Violation{Code: workboard.CodeStaleRevision}) {
		t.Fatalf("stale revision = %v", err)
	}
	noChange := workboard.ReviseBoardRequest{Version: 1, BoardID: created.BoardID, IdempotencyKey: "revise-operation-3", ExpectedRevision: 2, Title: &title}
	if _, err = store.ReviseWorkboard(ctx, noChange, actor, now); !errors.Is(err, &workboard.Violation{Code: workboard.CodeInvalid}) {
		t.Fatalf("no-op revision = %v", err)
	}
	snapshot, err = store.ReadWorkboard(ctx, created.BoardID, workboard.BoardSnapshotOptions{Limit: 1})
	if err != nil || snapshot.Board.Title != title || snapshot.Board.Description != "" || snapshot.Board.Revision != 2 || snapshot.Board.EventSequence != 2 {
		t.Fatalf("restarted snapshot=%+v err=%v", snapshot.Board, err)
	}
}

func TestWorkboardReviseRejectsArchivedBoard(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	actor := workboard.Actor{ID: "operator", Type: "operator"}
	now := time.Date(2026, 9, 9, 17, 0, 0, 0, time.UTC)
	created, err := store.CreateWorkboard(ctx, "session", createBoardRequest("create-archive-01", "Board", ""), actor, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ArchiveWorkboard(ctx, archiveBoardRequest(created.BoardID, "archive-board-001", 1), actor, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	title := "Forbidden"
	request := workboard.ReviseBoardRequest{Version: 1, BoardID: created.BoardID, IdempotencyKey: "revise-archived-1", ExpectedRevision: 2, Title: &title}
	if _, err = store.ReviseWorkboard(ctx, request, actor, now.Add(2*time.Minute)); !errors.Is(err, &workboard.Violation{Code: workboard.CodeIllegalTransition}) {
		t.Fatalf("archived revision = %v", err)
	}
}

func TestWorkboardBoardReplayRequiresImmutableEvent(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *Store, string)
	}{
		{"deleted", func(t *testing.T, store *Store, operationID string) {
			if _, err := store.db.Exec(`DELETE FROM workboard_events WHERE operation_id=?`, operationID); err != nil {
				t.Fatal(err)
			}
		}},
		{"corrupt", func(t *testing.T, store *Store, operationID string) {
			if _, err := store.db.Exec(`UPDATE workboard_events SET actor_id='tampered' WHERE operation_id=?`, operationID); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			actor := workboard.Actor{ID: "operator", Type: "operator"}
			now := time.Date(2026, 9, 9, 21, 0, 0, 0, time.UTC)
			created, err := store.CreateWorkboard(ctx, "session", createBoardRequest("journal-board-key", "Original", ""), actor, now)
			if err != nil {
				t.Fatal(err)
			}
			title := "Revised"
			request := workboard.ReviseBoardRequest{Version: 1, BoardID: created.BoardID, IdempotencyKey: "journal-revise-key", ExpectedRevision: 1, Title: &title}
			receipt, err := store.ReviseWorkboard(ctx, request, actor, now.Add(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(t, store, receipt.OperationID)
			if _, err = store.ReviseWorkboard(ctx, request, actor, now.Add(2*time.Minute)); !errors.Is(err, ErrWorkboardCorrupt) {
				t.Fatalf("replay with %s event = %v", test.name, err)
			}
		})
	}
}
