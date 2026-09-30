package telemetry

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/workboard"
)

type lifecycleVerifier struct {
	proof workboard.RecoveryProof
	err   error
	calls int
	mu    sync.Mutex
}

func (v *lifecycleVerifier) VerifyRecovery(context.Context, workboard.RecoverClaimRequest, workboard.Actor) (workboard.RecoveryProof, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.calls++
	return v.proof, v.err
}

func (v *lifecycleVerifier) count() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.calls
}

func TestWorkboardLifecycleClaimHeartbeatRecoverAndRestartReplay(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	card, boardID := readyLifecycleCard(t, ctx, store, now)
	worker := workboard.Actor{ID: "worker-a", Type: "worker"}
	verifier := verifiedLifecycleRecovery("proof-a", workboard.EffectFree)
	clock := card.UpdatedAt.Add(time.Second)
	service := newTestLifecycleService(t, store, worker, verifier, &clock)
	claimRequest := workboard.ClaimRequest{BoardID: boardID, CardID: card.ID, IdempotencyKey: "claim-operation-1", ExpectedCardRevision: card.Revision}
	claimed, err := service.Claim(ctx, claimRequest)
	if err != nil || claimed.BoardRevision != 4 || *claimed.CardRevision != 3 || *claimed.ClaimRevision != 1 {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	attemptID, claimID := currentLifecycleIDs(t, store, boardID, card.ID)
	clock = clock.Add(20 * time.Second)
	heartbeatRequest := workboard.HeartbeatRequest{BoardID: boardID, CardID: card.ID, AttemptID: attemptID, ClaimID: claimID,
		IdempotencyKey: "heartbeat-operation-1", ExpectedClaimRevision: 1}
	heartbeat, err := service.Heartbeat(ctx, heartbeatRequest)
	if err != nil || heartbeat.BoardRevision != 5 || *heartbeat.CardRevision != 3 || *heartbeat.ClaimRevision != 2 {
		t.Fatalf("heartbeat=%+v err=%v", heartbeat, err)
	}
	proof := recoveryIntent(verifier.proof)
	clock = clock.Add(20 * time.Second)
	recoveryService := newTestLifecycleService(t, store, workboard.Actor{ID: "supervisor", Type: "system"}, verifier, &clock)
	recoverRequest := workboard.RecoverClaimRequest{BoardID: boardID, CardID: card.ID, AttemptID: attemptID, ClaimID: claimID,
		IdempotencyKey: "recover-operation-1", ExpectedCardRevision: 3, ExpectedClaimRevision: 2, Proof: proof}
	recovered, err := recoveryService.Recover(ctx, recoverRequest)
	if err != nil || recovered.BoardRevision != 6 || *recovered.CardRevision != 4 || *recovered.ClaimRevision != 3 {
		t.Fatalf("recovered=%+v err=%v", recovered, err)
	}
	assertRecoveredLifecycleState(t, store, boardID, card.ID, attemptID, claimID)
	if verifier.count() != 1 {
		t.Fatalf("verifier calls=%d", verifier.count())
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock = clock.Add(time.Hour)
	service = newTestLifecycleService(t, store, worker, verifier, &clock)
	replayedClaim, err := service.Claim(ctx, claimRequest)
	if err != nil || !reflect.DeepEqual(replayedClaim, claimed) {
		t.Fatalf("claim replay=%+v err=%v", replayedClaim, err)
	}
	recoveryService = newTestLifecycleService(t, store, workboard.Actor{ID: "supervisor", Type: "system"}, verifier, &clock)
	replayedRecovery, err := recoveryService.Recover(ctx, recoverRequest)
	if err != nil || !reflect.DeepEqual(replayedRecovery, recovered) || verifier.count() != 1 {
		t.Fatalf("recovery replay=%+v calls=%d err=%v", replayedRecovery, verifier.count(), err)
	}
}

func TestWorkboardLifecycleRollbackAndRetry(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	card, boardID := readyLifecycleCard(t, ctx, store, now)
	worker := workboard.Actor{ID: "worker-a", Type: "worker"}
	verifier := verifiedLifecycleRecovery("proof-a", workboard.EffectFree)
	clock := card.UpdatedAt.Add(time.Second)
	service := newTestLifecycleService(t, store, worker, verifier, &clock)
	request := workboard.ClaimRequest{BoardID: boardID, CardID: card.ID, IdempotencyKey: "rollback-claim-01", ExpectedCardRevision: card.Revision}
	if _, err = store.db.Exec(`CREATE TRIGGER fail_claim_event BEFORE INSERT ON workboard_events WHEN NEW.kind='card.claim' BEGIN SELECT RAISE(ABORT,'claim event failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Claim(ctx, request); err == nil || !strings.Contains(err.Error(), "claim event failure") {
		t.Fatalf("rollback error=%v", err)
	}
	for table, want := range map[string]int{"workboard_attempts": 0, "workboard_claims": 0, "workboard_claim_heartbeats": 0} {
		var got int
		if err = store.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&got); err != nil || got != want {
			t.Fatalf("%s count=%d err=%v", table, got, err)
		}
	}
	stored, err := store.GetCard(ctx, boardID, card.ID)
	if err != nil || stored.State != workboard.Ready || stored.Revision != card.Revision {
		t.Fatalf("rolled back card=%+v err=%v", stored, err)
	}
	if _, err = store.db.Exec(`DROP TRIGGER fail_claim_event`); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Claim(ctx, request); err != nil {
		t.Fatal(err)
	}
}

func TestWorkboardLifecycleConcurrentExactClaimReplay(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	card, boardID := readyLifecycleCard(t, ctx, store, now)
	clock := card.UpdatedAt.Add(time.Second)
	service := newTestLifecycleService(t, store, workboard.Actor{ID: "worker-a", Type: "worker"}, verifiedLifecycleRecovery("proof-a", workboard.EffectFree), &clock)
	request := workboard.ClaimRequest{BoardID: boardID, CardID: card.ID, IdempotencyKey: "concurrent-claim-1", ExpectedCardRevision: card.Revision}
	const callers = 8
	results := make(chan workboard.OperationReceipt, callers)
	errorsOut := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			receipt, callErr := service.Claim(ctx, request)
			results <- receipt
			errorsOut <- callErr
		}()
	}
	wg.Wait()
	close(results)
	close(errorsOut)
	for callErr := range errorsOut {
		if callErr != nil {
			t.Fatalf("concurrent error=%v", callErr)
		}
	}
	var first workboard.OperationReceipt
	for receipt := range results {
		if first.OperationID == "" {
			first = receipt
		} else if !reflect.DeepEqual(first, receipt) {
			t.Fatalf("non-exact replay: first=%+v got=%+v", first, receipt)
		}
	}
	for table, want := range map[string]int{"workboard_attempts": 1, "workboard_claims": 1, "workboard_operations": 4, "workboard_events": 4} {
		var got int
		if err = store.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&got); err != nil || got != want {
			t.Fatalf("%s count=%d want=%d err=%v", table, got, want, err)
		}
	}
}

func TestWorkboardLifecycleHeartbeatAndRecoveryRollback(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	card, boardID := readyLifecycleCard(t, ctx, store, now)
	clock := card.UpdatedAt.Add(time.Second)
	worker := workboard.Actor{ID: "worker-a", Type: "worker"}
	verifier := verifiedLifecycleRecovery("rollback-proof", workboard.ResolvedNoReplay)
	workerService := newTestLifecycleService(t, store, worker, verifier, &clock)
	if _, err = workerService.Claim(ctx, workboard.ClaimRequest{BoardID: boardID, CardID: card.ID, IdempotencyKey: "rollback-seed-claim", ExpectedCardRevision: card.Revision}); err != nil {
		t.Fatal(err)
	}
	attemptID, claimID := currentLifecycleIDs(t, store, boardID, card.ID)
	clock = clock.Add(time.Second)
	heartbeatRequest := workboard.HeartbeatRequest{BoardID: boardID, CardID: card.ID, AttemptID: attemptID, ClaimID: claimID,
		IdempotencyKey: "rollback-heartbeat", ExpectedClaimRevision: 1}
	if _, err = store.db.Exec(`CREATE TRIGGER fail_heartbeat_event BEFORE INSERT ON workboard_events WHEN NEW.kind='claim.heartbeat' BEGIN SELECT RAISE(ABORT,'heartbeat event failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = workerService.Heartbeat(ctx, heartbeatRequest); err == nil || !strings.Contains(err.Error(), "heartbeat event failure") {
		t.Fatalf("heartbeat rollback error=%v", err)
	}
	assertLifecycleRevisions(t, store, attemptID, claimID, 1, 1)
	if _, err = store.db.Exec(`DROP TRIGGER fail_heartbeat_event`); err != nil {
		t.Fatal(err)
	}
	if _, err = workerService.Heartbeat(ctx, heartbeatRequest); err != nil {
		t.Fatal(err)
	}
	assertLifecycleRevisions(t, store, attemptID, claimID, 2, 2)
	clock = clock.Add(time.Second)
	recoverRequest := workboard.RecoverClaimRequest{BoardID: boardID, CardID: card.ID, AttemptID: attemptID, ClaimID: claimID,
		IdempotencyKey: "rollback-recovery", ExpectedCardRevision: card.Revision + 1, ExpectedClaimRevision: 2, Proof: recoveryIntent(verifier.proof)}
	recoveryService := newTestLifecycleService(t, store, workboard.Actor{ID: "supervisor", Type: "system"}, verifier, &clock)
	if _, err = store.db.Exec(`CREATE TRIGGER fail_recover_event BEFORE INSERT ON workboard_events WHEN NEW.kind='claim.recover' BEGIN SELECT RAISE(ABORT,'recovery event failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = recoveryService.Recover(ctx, recoverRequest); err == nil || !strings.Contains(err.Error(), "recovery event failure") {
		t.Fatalf("recovery rollback error=%v", err)
	}
	assertLifecycleRevisions(t, store, attemptID, claimID, 2, 2)
	var proofs, recoveries int
	if err = store.db.QueryRow(`SELECT count(*) FROM workboard_recovery_proofs`).Scan(&proofs); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow(`SELECT count(*) FROM workboard_recoveries`).Scan(&recoveries); err != nil || proofs != 0 || recoveries != 0 {
		t.Fatalf("proofs=%d recoveries=%d err=%v", proofs, recoveries, err)
	}
	if _, err = store.db.Exec(`DROP TRIGGER fail_recover_event`); err != nil {
		t.Fatal(err)
	}
	if _, err = recoveryService.Recover(ctx, recoverRequest); err != nil {
		t.Fatal(err)
	}
	if verifier.count() != 2 {
		t.Fatalf("uncommitted recovery verifier calls=%d", verifier.count())
	}
}

func TestWorkboardLifecycleRejectsWrongOwnerStaleAndChangedReplay(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	card, boardID := readyLifecycleCard(t, ctx, store, now)
	clock := card.UpdatedAt.Add(time.Second)
	verifier := verifiedLifecycleRecovery("proof-a", workboard.EffectFree)
	workerService := newTestLifecycleService(t, store, workboard.Actor{ID: "worker-a", Type: "worker"}, verifier, &clock)
	claimRequest := workboard.ClaimRequest{BoardID: boardID, CardID: card.ID, IdempotencyKey: "owner-claim-key01", ExpectedCardRevision: card.Revision}
	if _, err = workerService.Claim(ctx, claimRequest); err != nil {
		t.Fatal(err)
	}
	attemptID, claimID := currentLifecycleIDs(t, store, boardID, card.ID)
	clock = clock.Add(time.Second)
	heartbeat := workboard.HeartbeatRequest{BoardID: boardID, CardID: card.ID, AttemptID: attemptID, ClaimID: claimID,
		IdempotencyKey: "owner-heartbeat1", ExpectedClaimRevision: 1}
	otherWorker := newTestLifecycleService(t, store, workboard.Actor{ID: "worker-b", Type: "worker"}, verifier, &clock)
	if _, err = otherWorker.Heartbeat(ctx, heartbeat); !errors.Is(err, &workboard.Violation{Code: workboard.CodeLeaseOwner}) {
		t.Fatalf("wrong owner error=%v", err)
	}
	if _, err = workerService.Heartbeat(ctx, heartbeat); err != nil {
		t.Fatal(err)
	}
	heartbeat.IdempotencyKey = "stale-heartbeat-1"
	if _, err = workerService.Heartbeat(ctx, heartbeat); !errors.Is(err, &workboard.Violation{Code: workboard.CodeStaleRevision}) {
		t.Fatalf("stale heartbeat error=%v", err)
	}
	changedClaim := claimRequest
	changedClaim.ExpectedCardRevision++
	if _, err = workerService.Claim(ctx, changedClaim); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed replay error=%v", err)
	}
}

func readyLifecycleCard(t *testing.T, ctx context.Context, store *Store, now time.Time) (workboard.Card, string) {
	t.Helper()
	operator := workboard.Actor{ID: "operator", Type: "operator"}
	created, err := store.CreateWorkboard(ctx, "session", createBoardRequest("lifecycle-board-1", "Lifecycle", ""), operator, now)
	if err != nil {
		t.Fatal(err)
	}
	cardService, err := workboard.NewCardService(store, telemetryCardAuthority{authority: workboard.Authority{CreationScope: "session", Actor: operator}})
	if err != nil {
		t.Fatal(err)
	}
	createdCard, err := cardService.CreateCard(ctx, workboard.CreateCardRequest{BoardID: created.BoardID, IdempotencyKey: "lifecycle-card-01",
		ExpectedBoardRevision: 1, ExpectedGraphRevision: 1, Card: workboard.NewCard{Title: "Claim me", Priority: "normal", Labels: []string{},
			Dependencies: []string{}, Budget: workboardTestBudget(), Criteria: workboardTestCriteria()}})
	if err != nil {
		t.Fatal(err)
	}
	moved, err := cardService.MoveCard(ctx, workboard.MoveCardRequest{BoardID: created.BoardID, CardID: createdCard.ID, IdempotencyKey: "lifecycle-ready-1",
		TargetState: workboard.Ready, ExpectedBoardRevision: 2, ExpectedCardRevision: 1, ExpectedLayoutRevision: 2})
	if err != nil {
		t.Fatal(err)
	}
	return moved.Card, created.BoardID
}

func newTestLifecycleService(t *testing.T, store *Store, actor workboard.Actor, verifier *lifecycleVerifier, clock *time.Time) *workboard.LifecycleService {
	t.Helper()
	service, err := workboard.NewLifecycleService(store, telemetryCardAuthority{authority: workboard.Authority{CreationScope: "session", Actor: actor}}, verifier,
		func() time.Time { return *clock }, time.Minute, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func verifiedLifecycleRecovery(id string, resolution workboard.EffectResolution) *lifecycleVerifier {
	return &lifecycleVerifier{proof: workboard.RecoveryProof{StopProofID: id, TaskHeadDigest: strings.Repeat("1", 64),
		ProcessProofDigest: strings.Repeat("2", 64), EffectEvidenceDigest: strings.Repeat("3", 64), EffectResolution: resolution,
		TaskTerminal: true, ProcessStopped: true}}
}

func recoveryIntent(proof workboard.RecoveryProof) workboard.RecoveryIntent {
	return workboard.RecoveryIntent{StopProofID: proof.StopProofID, TaskHeadDigest: proof.TaskHeadDigest, ProcessProofDigest: proof.ProcessProofDigest,
		EffectEvidenceDigest: proof.EffectEvidenceDigest, EffectResolution: proof.EffectResolution}
}

func currentLifecycleIDs(t *testing.T, store *Store, boardID, cardID string) (string, string) {
	t.Helper()
	var attemptID, claimID string
	if err := store.db.QueryRow(`SELECT current_attempt_id,current_claim_id FROM workboard_cards WHERE board_id=? AND id=?`, boardID, cardID).Scan(&attemptID, &claimID); err != nil {
		t.Fatal(err)
	}
	return attemptID, claimID
}

func assertRecoveredLifecycleState(t *testing.T, store *Store, boardID, cardID, attemptID, claimID string) {
	t.Helper()
	var cardState, attemptState, claimState string
	var currentClaim any
	if err := store.db.QueryRow(`SELECT state,current_claim_id FROM workboard_cards WHERE board_id=? AND id=?`, boardID, cardID).Scan(&cardState, &currentClaim); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT state FROM workboard_attempts WHERE id=?`, attemptID).Scan(&attemptState); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT state FROM workboard_claims WHERE id=?`, claimID).Scan(&claimState); err != nil {
		t.Fatal(err)
	}
	if cardState != "ready" || currentClaim != nil || attemptState != "failed" || claimState != "released" {
		t.Fatalf("card=%s current=%v attempt=%s claim=%s", cardState, currentClaim, attemptState, claimState)
	}
}

func assertLifecycleRevisions(t *testing.T, store *Store, attemptID, claimID string, wantAttempt, wantClaim int64) {
	t.Helper()
	var attemptRevision, claimRevision int64
	if err := store.db.QueryRow(`SELECT revision FROM workboard_attempts WHERE id=?`, attemptID).Scan(&attemptRevision); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT revision FROM workboard_claims WHERE id=?`, claimID).Scan(&claimRevision); err != nil {
		t.Fatal(err)
	}
	if attemptRevision != wantAttempt || claimRevision != wantClaim {
		t.Fatalf("attempt revision=%d claim revision=%d", attemptRevision, claimRevision)
	}
}
