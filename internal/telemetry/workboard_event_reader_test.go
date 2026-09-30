package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/workboard"
)

func TestWorkboardEventPagesAreBoundedAndHighWaterStable(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	actor := workboard.Actor{ID: "operator", Type: "operator"}
	now := time.Date(2026, 9, 9, 18, 0, 0, 0, time.UTC)
	created, err := store.CreateWorkboard(ctx, "session", createBoardRequest("event-create-001", "One", "private description"), actor, now)
	if err != nil {
		t.Fatal(err)
	}
	for revision, title := range []string{"Two", "Three"} {
		value := title
		request := workboard.ReviseBoardRequest{Version: 1, BoardID: created.BoardID, IdempotencyKey: "event-revise-00" + string(rune('1'+revision)), ExpectedRevision: int64(revision + 1), Title: &value}
		if _, err = store.ReviseWorkboard(ctx, request, actor, now.Add(time.Duration(revision+1)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	first, err := store.ListWorkboardEvents(ctx, created.BoardID, workboard.BoardEventOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if first.Validate() != nil || first.HighWaterSequence != 3 || len(first.Items) != 1 || !first.HasMore || first.Items[0].Kind != workboard.BoardCreateAction {
		t.Fatalf("first=%+v", first)
	}
	latest := "Four"
	if _, err = store.ReviseWorkboard(ctx, workboard.ReviseBoardRequest{Version: 1, BoardID: created.BoardID, IdempotencyKey: "event-revise-003", ExpectedRevision: 3, Title: &latest}, actor, now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	second, err := store.ListWorkboardEvents(ctx, created.BoardID, workboard.BoardEventOptions{After: first.NextCursor, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if second.Validate() != nil || second.HighWaterSequence != 3 || len(second.Items) != 2 || second.HasMore || second.Items[1].Kind != workboard.BoardReviseAction {
		t.Fatalf("second=%+v", second)
	}
	fresh, err := store.ListWorkboardEvents(ctx, created.BoardID, workboard.BoardEventOptions{Limit: 100})
	if err != nil || fresh.HighWaterSequence != 4 || len(fresh.Items) != 4 {
		t.Fatalf("fresh=%+v err=%v", fresh, err)
	}
	encoded, err := jsonEvents(fresh.Items)
	if err != nil || strings.Contains(encoded, "private description") || strings.Contains(encoded, "event-create-001") {
		t.Fatalf("event stream leaked payload/key: %q err=%v", encoded, err)
	}
}

func TestWorkboardEventCursorRejectsForgeryAndCrossBoard(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	actor := workboard.Actor{ID: "operator", Type: "operator"}
	now := time.Date(2026, 9, 9, 19, 0, 0, 0, time.UTC)
	firstBoard, err := store.CreateWorkboard(ctx, "session", createBoardRequest("cursor-create-01", "First", ""), actor, now)
	if err != nil {
		t.Fatal(err)
	}
	title := "Second revision"
	if _, err = store.ReviseWorkboard(ctx, workboard.ReviseBoardRequest{Version: 1, BoardID: firstBoard.BoardID, IdempotencyKey: "cursor-revise-01", ExpectedRevision: 1, Title: &title}, actor, now); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListWorkboardEvents(ctx, firstBoard.BoardID, workboard.BoardEventOptions{Limit: 1})
	if err != nil || page.NextCursor == "" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	forged := page.NextCursor[:len(page.NextCursor)-1] + "0"
	if forged == page.NextCursor {
		forged = page.NextCursor[:len(page.NextCursor)-1] + "1"
	}
	if _, err = store.ListWorkboardEvents(ctx, firstBoard.BoardID, workboard.BoardEventOptions{After: forged, Limit: 1}); !errors.Is(err, ErrWorkboardCursor) {
		t.Fatalf("forged cursor = %v", err)
	}
	secondBoard, err := store.CreateWorkboard(ctx, "session", createBoardRequest("cursor-create-02", "Second", ""), actor, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ListWorkboardEvents(ctx, secondBoard.BoardID, workboard.BoardEventOptions{After: page.NextCursor, Limit: 1}); !errors.Is(err, ErrWorkboardCursor) {
		t.Fatalf("cross-board cursor = %v", err)
	}
}

func TestWorkboardEventTailFollowIsBoundedAndRestartSafe(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	actor := workboard.Actor{ID: "operator", Type: "operator"}
	now := time.Date(2026, 9, 9, 19, 30, 0, 0, time.UTC)
	created, err := store.CreateWorkboard(ctx, "session", createBoardRequest("tail-create-event-001", "One", ""), actor, now)
	if err != nil {
		t.Fatal(err)
	}
	for revision, title := range []string{"Two", "Three", "Four"} {
		value := title
		request := workboard.ReviseBoardRequest{Version: 1, BoardID: created.BoardID, IdempotencyKey: fmt.Sprintf("tail-revise-event-%03d", revision+1), ExpectedRevision: int64(revision + 1), Title: &value}
		if _, err = store.ReviseWorkboard(ctx, request, actor, now.Add(time.Duration(revision+1)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	idle, err := store.ListWorkboardEvents(ctx, created.BoardID, workboard.BoardEventOptions{Limit: 100, TailAfterSequence: 4})
	if err != nil || idle.Validate() != nil || idle.HighWaterSequence != 4 || len(idle.Items) != 1 || idle.Items[0].Sequence != 4 || idle.HasMore {
		t.Fatalf("idle tail=%+v err=%v", idle, err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	value := "Five"
	if _, err = store.ReviseWorkboard(ctx, workboard.ReviseBoardRequest{Version: 1, BoardID: created.BoardID, IdempotencyKey: "tail-revise-event-004", ExpectedRevision: 4, Title: &value}, actor, now.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	followed, err := store.ListWorkboardEvents(ctx, created.BoardID, workboard.BoardEventOptions{Limit: 100, TailAfterSequence: 4})
	if err != nil || followed.Validate() != nil || followed.HighWaterSequence != 5 || len(followed.Items) != 1 || followed.Items[0].Sequence != 5 || followed.HasMore {
		t.Fatalf("followed tail=%+v err=%v", followed, err)
	}
	bounded, err := store.ListWorkboardEvents(ctx, created.BoardID, workboard.BoardEventOptions{Limit: 1, TailAfterSequence: 3})
	if err != nil || bounded.Validate() != nil || len(bounded.Items) != 1 || bounded.Items[0].Sequence != 4 || !bounded.HasMore {
		t.Fatalf("bounded tail=%+v err=%v", bounded, err)
	}
	last, err := store.ListWorkboardEvents(ctx, created.BoardID, workboard.BoardEventOptions{Limit: 1, After: bounded.NextCursor})
	if err != nil || last.Validate() != nil || len(last.Items) != 1 || last.Items[0].Sequence != 5 || last.HasMore {
		t.Fatalf("tail continuation=%+v err=%v", last, err)
	}
}

func TestWorkboardEventTailFollowRejectsInvalidAnchorAndCorruption(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	actor := workboard.Actor{ID: "operator", Type: "operator"}
	created, err := store.CreateWorkboard(ctx, "session", createBoardRequest("tail-corrupt-event-001", "Board", ""), actor, time.Date(2026, 9, 9, 19, 45, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ListWorkboardEvents(ctx, created.BoardID, workboard.BoardEventOptions{Limit: 1, TailAfterSequence: 2}); !errors.Is(err, ErrWorkboardCursor) {
		t.Fatalf("future tail anchor=%v", err)
	}
	if _, err = store.ListWorkboardEvents(ctx, created.BoardID, workboard.BoardEventOptions{After: "cursor", Limit: 1, TailAfterSequence: 1}); err == nil {
		t.Fatalf("mixed cursor modes=%v", err)
	}
	if _, err = store.db.Exec(`UPDATE workboard_events SET actor_id='tampered' WHERE board_id=? AND sequence=1`, created.BoardID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ListWorkboardEvents(ctx, created.BoardID, workboard.BoardEventOptions{Limit: 1, TailAfterSequence: 1}); !errors.Is(err, ErrWorkboardCorrupt) {
		t.Fatalf("corrupt tail anchor=%v", err)
	}
}

func TestWorkboardEventReadRestartAndCorruptionFailClosed(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	actor := workboard.Actor{ID: "operator", Type: "operator"}
	now := time.Date(2026, 9, 9, 20, 0, 0, 0, time.UTC)
	created, err := store.CreateWorkboard(ctx, "session", createBoardRequest("restart-events-1", "Board", ""), actor, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	page, err := store.ListWorkboardEvents(ctx, created.BoardID, workboard.BoardEventOptions{Limit: 100})
	if err != nil || len(page.Items) != 1 || page.Items[0].Sequence != 1 {
		t.Fatalf("restart page=%+v err=%v", page, err)
	}
	var originalBody []byte
	if err = store.db.QueryRow(`SELECT body FROM workboard_events WHERE board_id=?`, created.BoardID).Scan(&originalBody); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`UPDATE workboard_events SET actor_id='tampered' WHERE board_id=?`, created.BoardID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ListWorkboardEvents(ctx, created.BoardID, workboard.BoardEventOptions{Limit: 100}); !errors.Is(err, ErrWorkboardCorrupt) {
		t.Fatalf("indexed corruption = %v", err)
	}
	if _, err = store.db.Exec(`UPDATE workboard_events SET actor_id='operator',body=? WHERE board_id=?`, originalBody, created.BoardID); err != nil {
		t.Fatal(err)
	}
	tamperedBody := append([]byte(nil), originalBody[:len(originalBody)-1]...)
	tamperedBody = append(tamperedBody, []byte(`,"private":"secret"}`)...)
	if _, err = store.db.Exec(`UPDATE workboard_events SET body=? WHERE board_id=?`, tamperedBody, created.BoardID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ListWorkboardEvents(ctx, created.BoardID, workboard.BoardEventOptions{Limit: 100}); !errors.Is(err, ErrWorkboardCorrupt) {
		t.Fatalf("noncanonical event body = %v", err)
	}
}

func TestWorkboardCardEventRoundTripAndIndexCorruption(t *testing.T) {
	ctx := context.Background()
	store, service, boardID := cardTestStore(t, ctx)
	card, err := service.CreateCard(ctx, workboard.CreateCardRequest{BoardID: boardID, IdempotencyKey: "card-event-roundtrip", ExpectedBoardRevision: 1, ExpectedGraphRevision: 1,
		Card: workboard.NewCard{Title: "Event card", Priority: "normal", Labels: []string{}, Dependencies: []string{}, Budget: workboardTestBudget(), Criteria: workboardTestCriteria()}})
	if err != nil {
		t.Fatal(err)
	}
	page, err := store.ListWorkboardEvents(ctx, boardID, workboard.BoardEventOptions{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if page.Validate() != nil || len(page.Items) != 2 || page.Items[1].Kind != workboard.CardCreateAction || page.Items[1].CardID != card.ID {
		t.Fatalf("card event page=%+v", page)
	}
	var indexedCardID string
	var body []byte
	if err = store.db.QueryRow(`SELECT card_id,body FROM workboard_events WHERE board_id=? AND sequence=2`, boardID).Scan(&indexedCardID, &body); err != nil {
		t.Fatal(err)
	}
	var canonical workboard.BoardEvent
	if strictJSON(body, &canonical) != nil || indexedCardID != card.ID || canonical.CardID != card.ID {
		t.Fatalf("card event index/body mismatch: index=%q body=%+v", indexedCardID, canonical)
	}
	if _, err = store.db.Exec(`UPDATE workboard_events SET card_id=NULL WHERE board_id=? AND sequence=2`, boardID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ListWorkboardEvents(ctx, boardID, workboard.BoardEventOptions{Limit: 100}); !errors.Is(err, ErrWorkboardCorrupt) {
		t.Fatalf("card event index corruption=%v", err)
	}
}

func jsonEvents(events []workboard.BoardEvent) (string, error) {
	body, err := json.Marshal(events)
	return string(body), err
}
