package telemetry

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

// A column that becomes empty may reuse its first append rank. Moving that
// card into a populated lifecycle column must allocate a new target rank rather
// than carrying the reused source rank into a uniqueness collision.
func TestSequentialClaimsAllocateDistinctInProgressRanks(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	first, boardID := readyLifecycleCard(t, ctx, store, now)
	now = time.Now().UTC().Add(time.Second)
	operator := workboard.Actor{ID: "operator", Type: "operator"}
	cards, err := workboard.NewCardService(store, telemetryCardAuthority{authority: workboard.Authority{CreationScope: "session", Actor: operator}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.ReadWorkboard(ctx, boardID, workboard.BoardSnapshotOptions{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	secondResult, err := cards.CreateCard(ctx, workboard.CreateCardRequest{BoardID: boardID, IdempotencyKey: "rank-second-card-01",
		ExpectedBoardRevision: snapshot.Board.Revision, ExpectedGraphRevision: snapshot.GraphRevision,
		Card: workboard.NewCard{Title: "Second", Priority: "normal", Labels: []string{}, Dependencies: []string{},
			Budget: workboardTestBudget(), Criteria: workboardTestCriteria()}})
	if err != nil {
		t.Fatal(err)
	}
	worker := newTestLifecycleService(t, store, workboard.Actor{ID: "worker-a", Type: "worker"},
		verifiedLifecycleRecovery("rank-transition-proof", workboard.EffectFree), &now)
	if _, err = worker.Claim(ctx, workboard.ClaimRequest{BoardID: boardID, CardID: first.ID,
		IdempotencyKey: "rank-first-claim-01", ExpectedCardRevision: first.Revision}); err != nil {
		t.Fatal(err)
	}
	snapshot, err = store.ReadWorkboard(ctx, boardID, workboard.BoardSnapshotOptions{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	secondReady, err := cards.MoveCard(ctx, workboard.MoveCardRequest{BoardID: boardID, CardID: secondResult.ID,
		IdempotencyKey: "rank-second-ready-01", TargetState: workboard.Ready, ExpectedBoardRevision: snapshot.Board.Revision,
		ExpectedCardRevision: secondResult.Revision, ExpectedLayoutRevision: snapshot.Board.LayoutRevision})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	if _, err = worker.Claim(ctx, workboard.ClaimRequest{BoardID: boardID, CardID: secondResult.ID,
		IdempotencyKey: "rank-second-claim-01", ExpectedCardRevision: secondReady.Card.Revision}); err != nil {
		t.Fatal(err)
	}
	rows, err := store.db.QueryContext(ctx, `SELECT rank FROM workboard_cards WHERE board_id=? AND state='in_progress' ORDER BY rank`, boardID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	ranks := []string{}
	for rows.Next() {
		var rank string
		if err = rows.Scan(&rank); err != nil {
			t.Fatal(err)
		}
		ranks = append(ranks, rank)
	}
	if err = rows.Err(); err != nil || len(ranks) != 2 || ranks[0] == ranks[1] || strings.TrimSpace(ranks[0]) == "" {
		t.Fatalf("in-progress ranks=%v err=%v", ranks, err)
	}
}
