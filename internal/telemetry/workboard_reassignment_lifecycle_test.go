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

type recoveredLifecycleFixture struct {
	boardID, cardID                string
	predecessorAttemptID           string
	predecessorClaimID, recoveryID string
	recoveredCardRevision          int64
}

func TestWorkboardReplacementClaimDerivesDurableReassignmentAndReplaysAfterRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	fixture := recoverLifecycleClaim(t, ctx, store, &clock, 3)
	replacement := newTestLifecycleService(t, store, workboard.Actor{ID: "replacement-worker", Type: "worker"},
		verifiedLifecycleRecovery("unused-replacement-proof", workboard.EffectFree), &clock)
	request := workboard.ClaimRequest{BoardID: fixture.boardID, CardID: fixture.cardID,
		IdempotencyKey: "replacement-claim-key-01", ExpectedCardRevision: fixture.recoveredCardRevision}
	receipt, err := replacement.Claim(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	successorAttemptID, successorClaimID := currentLifecycleIDs(t, store, fixture.boardID, fixture.cardID)
	assertStoredReassignment(t, store, fixture, successorAttemptID, successorClaimID, clock)
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock = clock.Add(time.Hour)
	replacement = newTestLifecycleService(t, store, workboard.Actor{ID: "replacement-worker", Type: "worker"},
		verifiedLifecycleRecovery("unused-replacement-proof", workboard.EffectFree), &clock)
	replayed, err := replacement.Claim(ctx, request)
	if err != nil || !reflect.DeepEqual(replayed, receipt) {
		t.Fatalf("replacement replay=%+v want=%+v err=%v", replayed, receipt, err)
	}
	assertStoredReassignment(t, store, fixture, successorAttemptID, successorClaimID, receipt.CreatedAt)
	assertReassignmentCount(t, store, 1)
}

func TestWorkboardOrdinaryClaimDoesNotCreateReassignment(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := time.Date(2026, 9, 9, 13, 0, 0, 0, time.UTC)
	card, boardID := readyLifecycleCard(t, ctx, store, clock)
	clock = card.UpdatedAt.Add(time.Second)
	worker := newTestLifecycleService(t, store, workboard.Actor{ID: "ordinary-worker", Type: "worker"},
		verifiedLifecycleRecovery("unused-ordinary-proof", workboard.EffectFree), &clock)
	if _, err = worker.Claim(ctx, workboard.ClaimRequest{BoardID: boardID, CardID: card.ID,
		IdempotencyKey: "ordinary-claim-key-001", ExpectedCardRevision: card.Revision}); err != nil {
		t.Fatal(err)
	}
	assertReassignmentCount(t, store, 0)
}

func TestWorkboardReplacementClaimRollsBackReassignmentWithTransaction(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := time.Date(2026, 9, 9, 14, 0, 0, 0, time.UTC)
	fixture := recoverLifecycleClaim(t, ctx, store, &clock, 3)
	replacement := newTestLifecycleService(t, store, workboard.Actor{ID: "rollback-replacement", Type: "worker"},
		verifiedLifecycleRecovery("unused-rollback-proof", workboard.EffectFree), &clock)
	request := workboard.ClaimRequest{BoardID: fixture.boardID, CardID: fixture.cardID,
		IdempotencyKey: "replacement-rollback-01", ExpectedCardRevision: fixture.recoveredCardRevision}
	if _, err = store.db.Exec(`CREATE TRIGGER fail_reassignment_claim_event BEFORE INSERT ON workboard_events
		WHEN NEW.kind='card.claim' BEGIN SELECT RAISE(ABORT,'replacement event failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = replacement.Claim(ctx, request); err == nil {
		t.Fatal("replacement transaction unexpectedly committed")
	}
	assertReassignmentCount(t, store, 0)
	assertLifecycleObjectCounts(t, store, 1, 1)
	card, err := store.GetCard(ctx, fixture.boardID, fixture.cardID)
	if err != nil || card.State != workboard.Ready || card.Revision != fixture.recoveredCardRevision {
		t.Fatalf("rolled back replacement card=%+v err=%v", card, err)
	}
	if _, err = store.db.Exec(`DROP TRIGGER fail_reassignment_claim_event`); err != nil {
		t.Fatal(err)
	}
	if _, err = replacement.Claim(ctx, request); err != nil {
		t.Fatal(err)
	}
	assertReassignmentCount(t, store, 1)
	assertLifecycleObjectCounts(t, store, 2, 2)
}

func TestWorkboardReplacementClaimRespectsAttemptLimitWithoutLineage(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := time.Date(2026, 9, 9, 15, 0, 0, 0, time.UTC)
	fixture := recoverLifecycleClaim(t, ctx, store, &clock, 1)
	replacement := newTestLifecycleService(t, store, workboard.Actor{ID: "limited-replacement", Type: "worker"},
		verifiedLifecycleRecovery("unused-limited-proof", workboard.EffectFree), &clock)
	_, err = replacement.Claim(ctx, workboard.ClaimRequest{BoardID: fixture.boardID, CardID: fixture.cardID,
		IdempotencyKey: "replacement-limit-key-01", ExpectedCardRevision: fixture.recoveredCardRevision})
	if !errors.Is(err, &workboard.Violation{Code: workboard.CodeIllegalTransition}) {
		t.Fatalf("attempt limit error=%v", err)
	}
	assertReassignmentCount(t, store, 0)
	assertLifecycleObjectCounts(t, store, 1, 1)
}

func TestWorkboardReplacementClaimRejectsCorruptRecoveryProof(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := time.Date(2026, 9, 9, 15, 30, 0, 0, time.UTC)
	fixture := recoverLifecycleClaim(t, ctx, store, &clock, 3)
	if _, err = store.db.Exec(`UPDATE workboard_recovery_proofs SET body=json_set(body,'$.task_head_digest',?) WHERE id=(
		SELECT proof_id FROM workboard_recoveries WHERE id=?)`, strings.Repeat("a", 64), fixture.recoveryID); err != nil {
		t.Fatal(err)
	}
	replacement := newTestLifecycleService(t, store, workboard.Actor{ID: "proof-drift-replacement", Type: "worker"},
		verifiedLifecycleRecovery("unused-proof-drift", workboard.EffectFree), &clock)
	_, err = replacement.Claim(ctx, workboard.ClaimRequest{BoardID: fixture.boardID, CardID: fixture.cardID,
		IdempotencyKey: "replacement-proof-drift", ExpectedCardRevision: fixture.recoveredCardRevision})
	if !errors.Is(err, ErrWorkboardCorrupt) {
		t.Fatalf("corrupt proof error=%v", err)
	}
	assertReassignmentCount(t, store, 0)
	assertLifecycleObjectCounts(t, store, 1, 1)
}

func TestWorkboardConcurrentReplacementClaimsCreateOneLineage(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := time.Date(2026, 9, 9, 16, 0, 0, 0, time.UTC)
	fixture := recoverLifecycleClaim(t, ctx, store, &clock, 3)
	workers := []*workboard.LifecycleService{
		newTestLifecycleService(t, store, workboard.Actor{ID: "replacement-a", Type: "worker"}, verifiedLifecycleRecovery("unused-a", workboard.EffectFree), &clock),
		newTestLifecycleService(t, store, workboard.Actor{ID: "replacement-b", Type: "worker"}, verifiedLifecycleRecovery("unused-b", workboard.EffectFree), &clock),
	}
	var wait sync.WaitGroup
	errorsOut := make(chan error, len(workers))
	for index, worker := range workers {
		wait.Add(1)
		go func(index int, worker *workboard.LifecycleService) {
			defer wait.Done()
			_, claimErr := worker.Claim(ctx, workboard.ClaimRequest{BoardID: fixture.boardID, CardID: fixture.cardID,
				IdempotencyKey:       []string{"replacement-race-key-a", "replacement-race-key-b"}[index],
				ExpectedCardRevision: fixture.recoveredCardRevision})
			errorsOut <- claimErr
		}(index, worker)
	}
	wait.Wait()
	close(errorsOut)
	succeeded, rejected := 0, 0
	for claimErr := range errorsOut {
		if claimErr == nil {
			succeeded++
		} else if errors.Is(claimErr, &workboard.Violation{Code: workboard.CodeStaleRevision}) ||
			errors.Is(claimErr, &workboard.Violation{Code: workboard.CodeIllegalTransition}) {
			rejected++
		} else {
			t.Fatalf("unexpected competing claim error=%v", claimErr)
		}
	}
	if succeeded != 1 || rejected != 1 {
		t.Fatalf("replacement claims succeeded=%d rejected=%d", succeeded, rejected)
	}
	assertReassignmentCount(t, store, 1)
	assertLifecycleObjectCounts(t, store, 2, 2)
	successorAttemptID, successorClaimID := currentLifecycleIDs(t, store, fixture.boardID, fixture.cardID)
	assertStoredReassignment(t, store, fixture, successorAttemptID, successorClaimID, clock)
}

func recoverLifecycleClaim(t *testing.T, ctx context.Context, store *Store, clock *time.Time, attemptLimit int) recoveredLifecycleFixture {
	t.Helper()
	operator := workboard.Actor{ID: "lineage-operator", Type: "operator"}
	created, err := store.CreateWorkboard(ctx, "session", createBoardRequest("lineage-board-key-01", "Lineage", ""), operator, *clock)
	if err != nil {
		t.Fatal(err)
	}
	cards, err := workboard.NewCardService(store, telemetryCardAuthority{authority: workboard.Authority{CreationScope: "session", Actor: operator}})
	if err != nil {
		t.Fatal(err)
	}
	createdCard, err := cards.CreateCard(ctx, workboard.CreateCardRequest{BoardID: created.BoardID,
		IdempotencyKey: "lineage-card-key-001", ExpectedBoardRevision: 1, ExpectedGraphRevision: 1,
		Card: workboard.NewCard{Title: "Recovered work", Priority: "normal", Labels: []string{}, Dependencies: []string{},
			Budget:   workboard.WorkBudget{AttemptLimit: attemptLimit, TimeLimitMS: 30_000, TokenLimit: 10_000, CostMicros: 1_000},
			Criteria: workboardTestCriteria()}})
	if err != nil {
		t.Fatal(err)
	}
	ready, err := cards.MoveCard(ctx, workboard.MoveCardRequest{BoardID: created.BoardID, CardID: createdCard.ID,
		IdempotencyKey: "lineage-ready-key-01", TargetState: workboard.Ready, ExpectedBoardRevision: 2,
		ExpectedCardRevision: 1, ExpectedLayoutRevision: 2})
	if err != nil {
		t.Fatal(err)
	}
	*clock = ready.Card.UpdatedAt.Add(time.Second)
	verifier := verifiedLifecycleRecovery("lineage-stop-proof", workboard.EffectFree)
	worker := newTestLifecycleService(t, store, workboard.Actor{ID: "predecessor-worker", Type: "worker"}, verifier, clock)
	if _, err = worker.Claim(ctx, workboard.ClaimRequest{BoardID: created.BoardID, CardID: createdCard.ID,
		IdempotencyKey: "lineage-first-claim-01", ExpectedCardRevision: ready.Card.Revision}); err != nil {
		t.Fatal(err)
	}
	predecessorAttemptID, predecessorClaimID := currentLifecycleIDs(t, store, created.BoardID, createdCard.ID)
	*clock = clock.Add(time.Second)
	supervisor := newTestLifecycleService(t, store, workboard.Actor{ID: "lineage-supervisor", Type: "system"}, verifier, clock)
	recovered, err := supervisor.Recover(ctx, workboard.RecoverClaimRequest{BoardID: created.BoardID, CardID: createdCard.ID,
		AttemptID: predecessorAttemptID, ClaimID: predecessorClaimID, IdempotencyKey: "lineage-recovery-key-1",
		ExpectedCardRevision: ready.Card.Revision + 1, ExpectedClaimRevision: 1, Proof: recoveryIntent(verifier.proof)})
	if err != nil || recovered.CardRevision == nil {
		t.Fatalf("recovery=%+v err=%v", recovered, err)
	}
	var recoveryID string
	if err = store.db.QueryRow(`SELECT id FROM workboard_recoveries WHERE board_id=? AND card_id=? AND attempt_id=? AND old_claim_id=?`,
		created.BoardID, createdCard.ID, predecessorAttemptID, predecessorClaimID).Scan(&recoveryID); err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(time.Second)
	return recoveredLifecycleFixture{boardID: created.BoardID, cardID: createdCard.ID,
		predecessorAttemptID: predecessorAttemptID, predecessorClaimID: predecessorClaimID,
		recoveryID: recoveryID, recoveredCardRevision: *recovered.CardRevision}
}

func assertStoredReassignment(t *testing.T, store *Store, fixture recoveredLifecycleFixture, successorAttemptID, successorClaimID string, createdAt time.Time) {
	t.Helper()
	var indexed workboard.ReassignmentRecord
	var storedBody []byte
	var indexedCreatedAt int64
	err := store.db.QueryRow(`SELECT recovery_id,board_id,card_id,predecessor_attempt_id,predecessor_claim_id,
		successor_attempt_id,successor_claim_id,created_at,body FROM workboard_reassignments WHERE recovery_id=?`, fixture.recoveryID).
		Scan(&indexed.RecoveryID, &indexed.BoardID, &indexed.CardID, &indexed.PredecessorAttemptID, &indexed.PredecessorClaimID,
			&indexed.SuccessorAttemptID, &indexed.SuccessorClaimID, &indexedCreatedAt, &storedBody)
	if err != nil {
		t.Fatal(err)
	}
	indexed.Version, indexed.CreatedAt = 1, time.Unix(0, indexedCreatedAt).UTC()
	want := workboard.ReassignmentRecord{Version: 1, RecoveryID: fixture.recoveryID, BoardID: fixture.boardID, CardID: fixture.cardID,
		PredecessorAttemptID: fixture.predecessorAttemptID, PredecessorClaimID: fixture.predecessorClaimID,
		SuccessorAttemptID: successorAttemptID, SuccessorClaimID: successorClaimID, CreatedAt: createdAt}
	var body workboard.ReassignmentRecord
	if json.Unmarshal(storedBody, &body) != nil || indexed.Validate() != nil || !reflect.DeepEqual(indexed, want) || !reflect.DeepEqual(body, want) {
		t.Fatalf("reassignment indexed=%+v body=%+v want=%+v", indexed, body, want)
	}
}

func assertReassignmentCount(t *testing.T, store *Store, want int) {
	t.Helper()
	var count int
	if err := store.db.QueryRow(`SELECT count(*) FROM workboard_reassignments`).Scan(&count); err != nil || count != want {
		t.Fatalf("reassignment count=%d want=%d err=%v", count, want, err)
	}
}

func assertLifecycleObjectCounts(t *testing.T, store *Store, attempts, claims int) {
	t.Helper()
	for table, want := range map[string]int{"workboard_attempts": attempts, "workboard_claims": claims} {
		var count int
		if err := store.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != want {
			t.Fatalf("%s count=%d want=%d err=%v", table, count, want, err)
		}
	}
}
