package telemetry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

func TestWorkboardSupervisionDerivesReadyRunningAndStalledWithoutMutation(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	store, card, boardID := supervisionFixture(t, now)
	defer store.Close()
	assignee := "supervision-worker"
	cards, err := workboard.NewCardService(store, telemetryCardAuthority{authority: workboard.Authority{
		CreationScope: "session", Actor: workboard.Actor{ID: "operator", Type: "operator"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	revised, err := cards.ReviseCard(ctx, workboard.ReviseCardRequest{BoardID: boardID, CardID: card.ID,
		IdempotencyKey: "supervision-assign-worker", ExpectedCardRevision: card.Revision,
		Patch: workboard.CardPatch{AssigneeID: &assignee}})
	if err != nil {
		t.Fatal(err)
	}
	card = revised.Card

	ready, err := store.ReadSupervisionPage(ctx, workboard.SupervisionQuery{BoardID: boardID, Limit: 10,
		ObservedAt: now.Add(time.Second), StaleBefore: now.Add(-time.Minute)})
	if err != nil || ready.Validate() != nil || len(ready.Items) != 1 || ready.Items[0].State != workboard.SupervisionReady ||
		ready.Items[0].Reason != workboard.SupervisionDependenciesSatisfied || ready.Items[0].AssigneeID != assignee || !ready.Items[0].Actions.Claim {
		t.Fatalf("ready=%+v err=%v", ready, err)
	}
	clock := card.UpdatedAt.Add(2 * time.Second)
	worker := newTestLifecycleService(t, store, workboard.Actor{ID: "supervision-worker", Type: "worker"},
		verifiedLifecycleRecovery("unused-supervision-proof", workboard.EffectFree), &clock)
	if _, err = worker.Claim(ctx, workboard.ClaimRequest{BoardID: boardID, CardID: card.ID,
		IdempotencyKey: "supervision-claim-01", ExpectedCardRevision: card.Revision}); err != nil {
		t.Fatal(err)
	}
	runningAt := clock.Add(20 * time.Second)
	running, err := store.ReadSupervisionPage(ctx, workboard.SupervisionQuery{BoardID: boardID, Limit: 10,
		ObservedAt: runningAt, StaleBefore: runningAt.Add(-30 * time.Second)})
	if err != nil || len(running.Items) != 1 || running.Items[0].State != workboard.SupervisionRunning ||
		running.Items[0].Reason != workboard.SupervisionLeaseHealthy || !running.Items[0].Actions.PauseRequest ||
		running.Items[0].AssigneeID != assignee || running.Items[0].WorkerID != assignee ||
		!running.Items[0].Actions.CancelRequest || running.Items[0].Actions.RecoveryCheck {
		t.Fatalf("running=%+v err=%v", running, err)
	}
	stalledAt := clock.Add(2 * time.Minute)
	stalled, err := store.ReadSupervisionPage(ctx, workboard.SupervisionQuery{BoardID: boardID, Limit: 10,
		ObservedAt: stalledAt, StaleBefore: stalledAt.Add(-30 * time.Second)})
	if err != nil || len(stalled.Items) != 1 || stalled.Items[0].State != workboard.SupervisionStalled ||
		stalled.Items[0].Reason != workboard.SupervisionLeaseExpired || !stalled.Items[0].Actions.RecoveryCheck ||
		stalled.Items[0].Actions.PauseRequest {
		t.Fatalf("stalled=%+v err=%v", stalled, err)
	}
	var boardRevision, claimRevision int64
	if err = store.db.QueryRow(`SELECT revision FROM workboard_boards WHERE id=?`, boardID).Scan(&boardRevision); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow(`SELECT revision FROM workboard_claims WHERE board_id=?`, boardID).Scan(&claimRevision); err != nil {
		t.Fatal(err)
	}
	if boardRevision != running.BoardRevision || claimRevision != running.Items[0].ClaimRevision {
		t.Fatalf("read mutated durable state: board=%d/%d claim=%d/%d", boardRevision, running.BoardRevision, claimRevision, running.Items[0].ClaimRevision)
	}
}

func TestWorkboardSupervisionDerivesOrphanFromTerminalTask(t *testing.T) {
	ctx := context.Background()
	fixture := newRecoveryFixture(t, "clean")
	defer fixture.store.Close()
	page, err := fixture.store.ReadSupervisionPage(ctx, workboard.SupervisionQuery{BoardID: fixture.boardID, Limit: 10,
		ObservedAt: fixture.clock, StaleBefore: fixture.clock.Add(-time.Minute)})
	if err != nil || len(page.Items) != 1 || page.Items[0].State != workboard.SupervisionOrphaned ||
		page.Items[0].Reason != workboard.SupervisionTaskCompleted || page.Items[0].TaskID != recoveryFixtureTask ||
		!page.Items[0].Actions.RecoveryCheck || page.Items[0].Actions.PauseRequest {
		t.Fatalf("orphaned=%+v err=%v", page, err)
	}
}

func TestWorkboardSupervisionCursorIsBoundedFrozenAndRevisionFenced(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	store, _, boardID := supervisionFixture(t, now)
	defer store.Close()
	service, err := workboard.NewCardService(store, telemetryCardAuthority{authority: workboard.Authority{
		CreationScope: "session", Actor: workboard.Actor{ID: "operator", Type: "operator"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	second := createCardForTest(t, ctx, service, boardID, "supervision-second-card", "Second", 3, 2, nil)
	if _, err = service.MoveCard(ctx, workboard.MoveCardRequest{BoardID: boardID, CardID: second.ID,
		IdempotencyKey: "supervision-second-ready", TargetState: workboard.Ready, ExpectedBoardRevision: 4,
		ExpectedCardRevision: second.Revision, ExpectedLayoutRevision: 4}); err != nil {
		t.Fatal(err)
	}
	first, err := store.ReadSupervisionPage(ctx, workboard.SupervisionQuery{BoardID: boardID, Limit: 1,
		ObservedAt: now.Add(time.Hour), StaleBefore: now})
	if err != nil || !first.HasMore || len(first.Items) != 1 || first.NextCursor == "" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	secondPage, err := store.ReadSupervisionPage(ctx, workboard.SupervisionQuery{BoardID: boardID, After: first.NextCursor, Limit: 1,
		ObservedAt: now.Add(2 * time.Hour), StaleBefore: now.Add(time.Hour)})
	if err != nil || secondPage.HasMore || len(secondPage.Items) != 1 || !secondPage.ObservedAt.Equal(first.ObservedAt) ||
		secondPage.Items[0].CardID <= first.Items[0].CardID {
		t.Fatalf("second=%+v err=%v", secondPage, err)
	}
	bad := first.NextCursor[:len(first.NextCursor)-1] + "x"
	if _, err = store.ReadSupervisionPage(ctx, workboard.SupervisionQuery{BoardID: boardID, After: bad, Limit: 1,
		ObservedAt: now, StaleBefore: now.Add(-time.Minute)}); !errors.Is(err, ErrWorkboardCursor) {
		t.Fatalf("tampered cursor err=%v", err)
	}
	if _, err = service.CreateCard(ctx, workboard.CreateCardRequest{BoardID: boardID, IdempotencyKey: "supervision-third-card",
		ExpectedBoardRevision: 5, ExpectedGraphRevision: 3, Card: workboard.NewCard{Title: "Third", Priority: "normal",
			Labels: []string{}, Dependencies: []string{}, Budget: workboardTestBudget(), Criteria: workboardTestCriteria()}}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReadSupervisionPage(ctx, workboard.SupervisionQuery{BoardID: boardID, After: first.NextCursor, Limit: 1,
		ObservedAt: now, StaleBefore: now.Add(-time.Minute)}); !errors.Is(err, ErrWorkboardCursor) {
		t.Fatalf("stale cursor err=%v", err)
	}
}

func supervisionFixture(t *testing.T, now time.Time) (*Store, workboard.Card, string) {
	t.Helper()
	store, err := Open(context.Background(), t.TempDir()+"/state.db")
	if err != nil {
		t.Fatal(err)
	}
	card, boardID := readyLifecycleCard(t, context.Background(), store, now)
	return store, card, boardID
}
