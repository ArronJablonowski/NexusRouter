package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

func TestWorkboardCreateReadListAndRestartReplay(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 9, 12, 0, 0, 123, time.UTC)
	request := createBoardRequest("0123456789abcdef", "Release board", "Bounded work")
	actor := workboard.Actor{ID: "operator-1", Type: "operator"}
	receipt, err := store.CreateWorkboard(ctx, "browser-session-1", request, actor, now)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Validate() != nil || receipt.BoardRevision != 1 || receipt.EventCount != 1 {
		t.Fatalf("invalid receipt: %+v", receipt)
	}
	var eventBody, boardBody, storedResponse []byte
	if err = store.db.QueryRow(`SELECT body FROM workboard_events WHERE operation_id=?`, receipt.OperationID).Scan(&eventBody); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow(`SELECT body FROM workboard_boards WHERE id=?`, receipt.BoardID).Scan(&boardBody); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow(`SELECT response FROM workboard_operations WHERE operation_id=?`, receipt.OperationID).Scan(&storedResponse); err != nil {
		t.Fatal(err)
	}
	if receipt.TransactionBytes <= len(eventBody)+len(boardBody)+len(storedResponse) {
		t.Fatalf("transaction bytes omit canonical columns: %d", receipt.TransactionBytes)
	}
	snapshot, err := store.ReadWorkboard(ctx, receipt.BoardID, workboard.BoardSnapshotOptions{Limit: workboard.MaxPageItems})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Validate() != nil || snapshot.Board.Title != "Release board" || len(snapshot.Columns) != 7 || len(snapshot.Cards) != 0 {
		t.Fatalf("invalid snapshot: %+v", snapshot)
	}
	for i, column := range snapshot.Columns {
		if column.State != canonicalWorkboardColumns[i].state || column.Title != canonicalWorkboardColumns[i].title {
			t.Fatalf("column %d = %+v", i, column)
		}
	}
	page, err := store.ListWorkboards(ctx, workboard.BoardListOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if page.Validate() != nil || len(page.Items) != 1 || page.Items[0] != snapshot.Board {
		t.Fatalf("invalid page: %+v", page)
	}
	var operationCount, eventCount, columnCount int
	var keyDigest string
	var response []byte
	if err = store.db.QueryRow(`SELECT count(*),min(key_digest),min(response) FROM workboard_operations`).Scan(&operationCount, &keyDigest, &response); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow(`SELECT count(*) FROM workboard_events`).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow(`SELECT count(*) FROM workboard_columns`).Scan(&columnCount); err != nil {
		t.Fatal(err)
	}
	if operationCount != 1 || eventCount != 1 || columnCount != 7 || keyDigest == request.IdempotencyKey || strings.Contains(string(response), request.IdempotencyKey) {
		t.Fatalf("durability counts=%d/%d/%d key=%q response=%q", operationCount, eventCount, columnCount, keyDigest, response)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	replayed, err := store.CreateWorkboard(ctx, "browser-session-1", request, actor, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(replayed, receipt) {
		t.Fatalf("restart replay changed receipt\n got: %+v\nwant: %+v", replayed, receipt)
	}
	changed := createBoardRequest(request.IdempotencyKey, "Different board", "Bounded work")
	if _, err = store.CreateWorkboard(ctx, "browser-session-1", changed, actor, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("same key different body error = %v", err)
	}
}

func TestWorkboardArchiveCASReplayAndRollback(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 9, 9, 13, 0, 0, 0, time.UTC)
	actor := workboard.Actor{ID: "operator", Type: "operator"}
	created, err := store.CreateWorkboard(ctx, "session", createBoardRequest("create-key-00001", "Board", ""), actor, now)
	if err != nil {
		t.Fatal(err)
	}
	archive := archiveBoardRequest(created.BoardID, "archive-key-0001", 1)
	if _, err = store.ArchiveWorkboard(ctx, archiveBoardRequest(created.BoardID, "stale-key-0000001", 2), actor, now); !errors.Is(err, &workboard.Violation{Code: workboard.CodeStaleRevision}) {
		t.Fatalf("stale error = %v", err)
	}
	assertWorkboardCounts(t, store, 1, 1, 1)
	if _, err = store.db.Exec(`CREATE TRIGGER test_fail_archive BEFORE INSERT ON workboard_events WHEN NEW.kind='board.archive' BEGIN SELECT RAISE(ABORT,'injected event failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ArchiveWorkboard(ctx, archive, actor, now.Add(time.Minute)); err == nil || !strings.Contains(err.Error(), "injected event failure") {
		t.Fatalf("injected failure = %v", err)
	}
	assertWorkboardCounts(t, store, 1, 1, 1)
	snapshot, err := store.ReadWorkboard(ctx, created.BoardID, workboard.BoardSnapshotOptions{Limit: 1})
	if err != nil || snapshot.Board.State != "active" || snapshot.Board.Revision != 1 || snapshot.Board.EventSequence != 1 {
		t.Fatalf("archive rollback projection=%+v err=%v", snapshot.Board, err)
	}
	if _, err = store.db.Exec(`DROP TRIGGER test_fail_archive`); err != nil {
		t.Fatal(err)
	}
	receipt, err := store.ArchiveWorkboard(ctx, archive, actor, now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if receipt.BoardRevision != 2 || receipt.FirstSequence != 2 {
		t.Fatalf("archive receipt = %+v", receipt)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := store.ArchiveWorkboard(ctx, archive, actor, now.Add(3*time.Minute))
	if err != nil || !reflect.DeepEqual(replay, receipt) {
		t.Fatalf("archive replay=%+v err=%v", replay, err)
	}
	changed := archive
	changed.ExpectedRevision = 2
	if _, err = store.ArchiveWorkboard(ctx, changed, actor, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("replay-before-stale conflict = %v", err)
	}
	if _, err = store.ArchiveWorkboard(ctx, archiveBoardRequest(created.BoardID, "second-archive01", 2), actor, now); !errors.Is(err, &workboard.Violation{Code: workboard.CodeIllegalTransition}) {
		t.Fatalf("second archive = %v", err)
	}
	assertWorkboardCounts(t, store, 1, 2, 2)
	snapshot, err = store.ReadWorkboard(ctx, created.BoardID, workboard.BoardSnapshotOptions{Limit: 1})
	if err != nil || snapshot.Board.State != "archived" || snapshot.Board.Revision != 2 || snapshot.Board.EventSequence != 2 {
		t.Fatalf("archived projection=%+v err=%v", snapshot.Board, err)
	}
}

func TestWorkboardCreateFailureRollsBackAndRetrySucceeds(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err = store.db.Exec(`CREATE TRIGGER test_fail_create BEFORE INSERT ON workboard_events WHEN NEW.kind='board.create' BEGIN SELECT RAISE(ABORT,'injected create failure'); END`); err != nil {
		t.Fatal(err)
	}
	request := createBoardRequest("rollback-key-001", "Rollback", "")
	actor := workboard.Actor{ID: "operator", Type: "operator"}
	if _, err = store.CreateWorkboard(ctx, "session", request, actor, time.Now().UTC()); err == nil {
		t.Fatal("expected failure")
	}
	assertWorkboardCounts(t, store, 0, 0, 0)
	var columns int
	if err = store.db.QueryRow(`SELECT count(*) FROM workboard_columns`).Scan(&columns); err != nil || columns != 0 {
		t.Fatalf("columns=%d err=%v", columns, err)
	}
	if _, err = store.db.Exec(`DROP TRIGGER test_fail_create`); err != nil {
		t.Fatal(err)
	}
	if _, err = store.CreateWorkboard(ctx, "session", request, actor, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	assertWorkboardCounts(t, store, 1, 1, 1)
}

func TestWorkboardConcurrentExactCreateConverges(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	request := createBoardRequest("concurrent-key-01", "Concurrent", "")
	actor := workboard.Actor{ID: "operator", Type: "operator"}
	now := time.Date(2026, 9, 9, 14, 0, 0, 0, time.UTC)
	const callers = 8
	receipts := make([]workboard.OperationReceipt, callers)
	errorsSeen := make([]error, callers)
	var group sync.WaitGroup
	for index := range receipts {
		group.Add(1)
		go func() {
			defer group.Done()
			receipts[index], errorsSeen[index] = store.CreateWorkboard(ctx, "session", request, actor, now)
		}()
	}
	group.Wait()
	for index, callErr := range errorsSeen {
		if callErr != nil {
			t.Fatalf("caller %d: %v", index, callErr)
		}
		if !reflect.DeepEqual(receipts[index], receipts[0]) {
			t.Fatalf("caller %d receipt diverged", index)
		}
	}
	assertWorkboardCounts(t, store, 1, 1, 1)
}

func TestWorkboardRequestDigestExcludesIdempotencyKey(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	actor := workboard.Actor{ID: "operator", Type: "operator"}
	now := time.Date(2026, 9, 9, 14, 0, 0, 0, time.UTC)
	first, err := store.CreateWorkboard(ctx, "scope-a", createBoardRequest("semantic-key-001", "Same", "Same"), actor, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateWorkboard(ctx, "scope-b", createBoardRequest("semantic-key-002", "Same", "Same"), actor, now)
	if err != nil {
		t.Fatal(err)
	}
	if first.RequestDigest != second.RequestDigest {
		t.Fatalf("transport key changed semantic digest: %s != %s", first.RequestDigest, second.RequestDigest)
	}
}

func TestWorkboardListPaginationAndCorruptionFailClosed(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	actor := workboard.Actor{ID: "operator", Type: "operator"}
	for i, key := range []string{"pagination-key-01", "pagination-key-02", "pagination-key-03"} {
		if _, err = store.CreateWorkboard(ctx, "session", createBoardRequest(key, "Board "+string(rune('A'+i)), ""), actor, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	first, err := store.ListWorkboards(ctx, workboard.BoardListOptions{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 || !first.HasMore || first.NextCursor == "" || first.Validate() != nil {
		t.Fatalf("first page = %+v", first)
	}
	if _, err = store.CreateWorkboard(ctx, "session", createBoardRequest("pagination-key-04", "Late board", ""), actor, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	second, err := store.ListWorkboards(ctx, workboard.BoardListOptions{After: first.NextCursor, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	// IDs are intentionally random; the durable rowid fence, not lexical ID
	// order, keeps the late board outside this traversal.
	if len(second.Items) != 1 || second.HasMore {
		t.Fatalf("second page = %+v", second)
	}
	if _, err = store.ListWorkboards(ctx, workboard.BoardListOptions{After: first.NextCursor, Limit: 2, State: "active"}); !errors.Is(err, ErrWorkboardCursor) {
		t.Fatalf("cursor filter substitution = %v", err)
	}
	forged := "A" + first.NextCursor[1:]
	if forged == first.NextCursor {
		forged = "B" + first.NextCursor[1:]
	}
	if _, err = store.ListWorkboards(ctx, workboard.BoardListOptions{After: forged, Limit: 2}); !errors.Is(err, ErrWorkboardCursor) {
		t.Fatalf("forged cursor = %v", err)
	}
	other, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err = other.ListWorkboards(ctx, workboard.BoardListOptions{After: first.NextCursor, Limit: 2}); !errors.Is(err, ErrWorkboardCursor) {
		t.Fatalf("cross-store cursor = %v", err)
	}
	if _, err = store.ListWorkboards(ctx, workboard.BoardListOptions{After: "bad/cursor", Limit: 1}); !errors.Is(err, ErrWorkboardCursor) {
		t.Fatalf("bad cursor = %v", err)
	}
	if _, err = store.ReadWorkboard(ctx, first.Items[0].ID, workboard.BoardSnapshotOptions{After: "not-base64!", Limit: 1}); !errors.Is(err, ErrWorkboardCursor) {
		t.Fatalf("bad card cursor = %v", err)
	}
	if _, err = store.db.Exec(`UPDATE workboard_boards SET body='{}' WHERE id=?`, first.Items[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReadWorkboard(ctx, first.Items[0].ID, workboard.BoardSnapshotOptions{Limit: 1}); !errors.Is(err, ErrWorkboardCorrupt) {
		t.Fatalf("corrupt read = %v", err)
	}
	if _, err = store.ListWorkboards(ctx, workboard.BoardListOptions{Limit: 3}); !errors.Is(err, ErrWorkboardCorrupt) {
		t.Fatalf("corrupt list = %v", err)
	}
}

func TestWorkboardCardProjectionRejectsIndexedBodyDivergence(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 9, 9, 16, 0, 0, 0, time.UTC)
	created, err := store.CreateWorkboard(ctx, "scope", createBoardRequest("card-projection-1", "Board", ""), workboard.Actor{ID: "operator", Type: "operator"}, now)
	if err != nil {
		t.Fatal(err)
	}
	board, _, _, err := readBoardRow(ctx, store.db, created.BoardID)
	if err != nil {
		t.Fatal(err)
	}
	board.CardCount = 1
	board.UpdatedAt = now.Add(time.Second)
	boardBody, _ := json.Marshal(board)
	if _, err = store.db.Exec(`UPDATE workboard_boards SET card_count=1,updated_at=?,body=? WHERE id=?`, board.UpdatedAt.UnixNano(), boardBody, board.ID); err != nil {
		t.Fatal(err)
	}
	body := storedWorkboardCard{Version: 1, ID: "card", BoardID: board.ID, Revision: 1, CriteriaRevision: 1, State: "backlog", Rank: "a",
		Title: "Card", Description: "description", Priority: "normal", Labels: []string{}, Dependencies: []string{},
		Budget: storedWorkboardBudget{AttemptLimit: 1}, Criteria: []storedWorkboardCriterion{}, CreatedAt: now, UpdatedAt: now}
	expectedIndex := workboardCardIndex{Ordinal: 0, ColumnState: "backlog", ID: "card", Revision: 1, CriteriaRevision: 1, State: "backlog", Rank: "a",
		Title: "Card", Description: "description", Priority: "normal", AttemptLimit: 1, CreatedAt: now.UnixNano(), UpdatedAt: now.UnixNano()}
	if !storedCardMatches(expectedIndex, body, board.ID) {
		t.Fatal("valid fixture does not match normalized index")
	}
	encoded, _ := json.Marshal(body)
	if _, err = store.db.Exec(`INSERT INTO workboard_cards
		(id,board_id,revision,criteria_revision,state,rank,title,description,priority,parent_id,assignee_id,block_reason,remaining_dependencies,attempt_count,
		attempt_limit,time_limit_ms,token_limit,cost_micros,current_attempt_id,current_claim_id,acceptance_id,cancel_requested,pause_requested,created_at,updated_at,body)
		VALUES(?,?,?,?,?,?,?,?,?,NULL,NULL,NULL,0,0,1,0,0,0,NULL,NULL,NULL,0,0,?,?,?)`, body.ID, body.BoardID, 1, 1, body.State, body.Rank, body.Title, body.Description, body.Priority, now.UnixNano(), now.UnixNano(), encoded); err != nil {
		t.Fatal(err)
	}
	if snapshot, readErr := store.ReadWorkboard(ctx, board.ID, workboard.BoardSnapshotOptions{Limit: 10}); readErr != nil || len(snapshot.Cards) != 1 {
		t.Fatalf("valid card projection err=%v snapshot=%+v", readErr, snapshot)
	}
	cases := map[string]func(*storedWorkboardCard){
		"id":                func(card *storedWorkboardCard) { card.ID = "other-card" },
		"board":             func(card *storedWorkboardCard) { card.BoardID = "other-board" },
		"revision":          func(card *storedWorkboardCard) { card.Revision++ },
		"criteria revision": func(card *storedWorkboardCard) { card.CriteriaRevision++ },
		"state/column":      func(card *storedWorkboardCard) { card.State = "ready" },
		"rank":              func(card *storedWorkboardCard) { card.Rank = "b" },
		"title":             func(card *storedWorkboardCard) { card.Title = "Changed" },
		"description":       func(card *storedWorkboardCard) { card.Description = "changed" },
		"priority":          func(card *storedWorkboardCard) { card.Priority = "high" },
		"parent":            func(card *storedWorkboardCard) { card.ParentID = "parent" },
		"assignee":          func(card *storedWorkboardCard) { card.AssigneeID = "other" },
		"block reason":      func(card *storedWorkboardCard) { card.BlockReason = "reason" },
		"remaining deps":    func(card *storedWorkboardCard) { card.Dependencies = []string{"dep"}; card.RemainingDependencies = 1 },
		"attempt count":     func(card *storedWorkboardCard) { card.AttemptCount = 1 },
		"attempt budget":    func(card *storedWorkboardCard) { card.Budget.AttemptLimit = 2 },
		"time budget":       func(card *storedWorkboardCard) { card.Budget.TimeLimitMS = 1 },
		"token budget":      func(card *storedWorkboardCard) { card.Budget.TokenLimit = 1 },
		"cost budget":       func(card *storedWorkboardCard) { card.Budget.CostMicros = 1 },
		"current attempt":   func(card *storedWorkboardCard) { card.CurrentAttemptID = "attempt" },
		"current claim":     func(card *storedWorkboardCard) { card.CurrentClaimID = "claim" },
		"acceptance":        func(card *storedWorkboardCard) { card.AcceptanceID = "acceptance" },
		"cancel":            func(card *storedWorkboardCard) { card.CancelRequested = true },
		"pause":             func(card *storedWorkboardCard) { card.PauseRequested = true },
		"created":           func(card *storedWorkboardCard) { card.CreatedAt = card.CreatedAt.Add(time.Second) },
		"updated":           func(card *storedWorkboardCard) { card.UpdatedAt = card.UpdatedAt.Add(time.Second) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			changed := body
			mutate(&changed)
			changedBody, _ := json.Marshal(changed)
			if _, updateErr := store.db.Exec(`UPDATE workboard_cards SET body=? WHERE id='card'`, changedBody); updateErr != nil {
				t.Fatal(updateErr)
			}
			if _, readErr := store.ReadWorkboard(ctx, board.ID, workboard.BoardSnapshotOptions{Limit: 10}); !errors.Is(readErr, ErrWorkboardCorrupt) {
				t.Fatalf("divergence accepted: %v", readErr)
			}
			if _, updateErr := store.db.Exec(`UPDATE workboard_cards SET body=? WHERE id='card'`, encoded); updateErr != nil {
				t.Fatal(updateErr)
			}
		})
	}
}

func createBoardRequest(key, title, description string) workboard.CreateBoardRequest {
	return workboard.CreateBoardRequest{Version: 1, IdempotencyKey: key, Title: title, Description: description}
}

func archiveBoardRequest(boardID, key string, revision int64) workboard.ArchiveBoardRequest {
	return workboard.ArchiveBoardRequest{Version: 1, BoardID: boardID, IdempotencyKey: key, ExpectedRevision: revision}
}

func assertWorkboardCounts(t *testing.T, store *Store, boards, operations, events int) {
	t.Helper()
	for table, want := range map[string]int{"workboard_boards": boards, "workboard_operations": operations, "workboard_events": events} {
		var got int
		if err := store.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&got); err != nil || got != want {
			t.Fatalf("%s count=%d want=%d err=%v", table, got, want, err)
		}
	}
}
