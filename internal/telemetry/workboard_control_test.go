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

type controlVerifier struct {
	proof workboard.RecoveryProof
	err   error
	mu    sync.Mutex
	calls int
}

func (v *controlVerifier) VerifyControlStop(context.Context, workboard.FinalizeCancelRequest, workboard.Actor) (workboard.RecoveryProof, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.calls++
	return v.proof, v.err
}

func (v *controlVerifier) count() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.calls
}

func TestWorkboardControlTransitionsRestartAndExactReplay(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	card, boardID := readyLifecycleCard(t, ctx, store, time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC))
	clock := card.UpdatedAt.Add(time.Second)
	worker := workboard.Actor{ID: "worker-a", Type: "worker"}
	proof := workboard.RecoveryProof{StopProofID: "cancel-proof", TaskHeadDigest: strings.Repeat("1", 64), ProcessProofDigest: strings.Repeat("2", 64),
		EffectEvidenceDigest: strings.Repeat("3", 64), EffectResolution: workboard.ResolvedNoReplay, TaskTerminal: true, ProcessStopped: true}
	verifier := &controlVerifier{proof: proof}
	workerLifecycle := newTestLifecycleService(t, store, worker, verifiedLifecycleRecovery("unused-proof", workboard.EffectFree), &clock)
	if _, err = workerLifecycle.Claim(ctx, workboard.ClaimRequest{BoardID: boardID, CardID: card.ID, IdempotencyKey: "control-claim-key", ExpectedCardRevision: card.Revision}); err != nil {
		t.Fatal(err)
	}
	attemptID, claimID := currentLifecycleIDs(t, store, boardID, card.ID)
	operatorService := newTestControlService(t, store, workboard.Actor{ID: "operator", Type: "operator"}, verifier, &clock)
	pauseRequest := workboard.RequestCardControl{BoardID: boardID, CardID: card.ID, IdempotencyKey: "pause-request-key", ExpectedCardRevision: card.Revision + 1}
	clock = clock.Add(time.Second)
	paused, err := operatorService.RequestPause(ctx, pauseRequest)
	if err != nil || paused.ClaimRevision != nil || *paused.CardRevision != card.Revision+2 {
		t.Fatalf("pause=%+v err=%v", paused, err)
	}
	workerService := newTestControlService(t, store, worker, verifier, &clock)
	clock = clock.Add(time.Second)
	blockRequest := workboard.ClaimCardControl{BoardID: boardID, CardID: card.ID, AttemptID: attemptID, ClaimID: claimID,
		IdempotencyKey: "block-request-key", ExpectedCardRevision: *paused.CardRevision, ExpectedClaimRevision: 1, ReasonCode: "waiting_on_input"}
	blocked, err := workerService.Block(ctx, blockRequest)
	if err != nil || *blocked.ClaimRevision != 1 {
		t.Fatalf("block=%+v err=%v", blocked, err)
	}
	clock = clock.Add(time.Second)
	unblockRequest := blockRequest
	unblockRequest.IdempotencyKey = "unblock-request-1"
	unblockRequest.ExpectedCardRevision = *blocked.CardRevision
	unblocked, err := workerService.Unblock(ctx, unblockRequest)
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Second)
	cancelRequest := workboard.RequestCardControl{BoardID: boardID, CardID: card.ID, IdempotencyKey: "cancel-request-key", ExpectedCardRevision: *unblocked.CardRevision}
	cancelRequested, err := operatorService.RequestCancel(ctx, cancelRequest)
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Second)
	finalizeRequest := workboard.FinalizeCancelRequest{BoardID: boardID, CardID: card.ID, AttemptID: attemptID, ClaimID: claimID,
		IdempotencyKey: "cancel-finalize-1", ExpectedCardRevision: *cancelRequested.CardRevision, ExpectedClaimRevision: 1, Proof: recoveryIntent(proof)}
	finalized, err := operatorService.FinalizeCancel(ctx, finalizeRequest)
	if err != nil || *finalized.ClaimRevision != 2 || verifier.count() != 1 {
		t.Fatalf("finalized=%+v calls=%d err=%v", finalized, verifier.count(), err)
	}
	assertCanceledControlState(t, store, boardID, card.ID, attemptID, claimID)
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock = clock.Add(time.Hour)
	operatorService = newTestControlService(t, store, workboard.Actor{ID: "operator", Type: "operator"}, verifier, &clock)
	if replay, replayErr := operatorService.RequestPause(ctx, pauseRequest); replayErr != nil || !reflect.DeepEqual(replay, paused) {
		t.Fatalf("pause replay=%+v err=%v", replay, replayErr)
	}
	if replay, replayErr := operatorService.FinalizeCancel(ctx, finalizeRequest); replayErr != nil || !reflect.DeepEqual(replay, finalized) || verifier.count() != 1 {
		t.Fatalf("finalize replay=%+v calls=%d err=%v", replay, verifier.count(), replayErr)
	}
}

func TestWorkboardCancelFinalizeRollbackAndControlFences(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	card, boardID := readyLifecycleCard(t, ctx, store, time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC))
	clock := card.UpdatedAt.Add(time.Second)
	worker := workboard.Actor{ID: "worker-a", Type: "worker"}
	lifecycle := newTestLifecycleService(t, store, worker, verifiedLifecycleRecovery("unused-proof", workboard.EffectFree), &clock)
	if _, err = lifecycle.Claim(ctx, workboard.ClaimRequest{BoardID: boardID, CardID: card.ID, IdempotencyKey: "rollback-control-claim", ExpectedCardRevision: card.Revision}); err != nil {
		t.Fatal(err)
	}
	attemptID, claimID := currentLifecycleIDs(t, store, boardID, card.ID)
	proof := workboard.RecoveryProof{StopProofID: "rollback-cancel-proof", TaskHeadDigest: strings.Repeat("1", 64), ProcessProofDigest: strings.Repeat("2", 64),
		EffectEvidenceDigest: strings.Repeat("3", 64), EffectResolution: workboard.EffectFree, TaskTerminal: true, ProcessStopped: true}
	verifier := &controlVerifier{proof: proof}
	operator := newTestControlService(t, store, workboard.Actor{ID: "operator", Type: "operator"}, verifier, &clock)
	clock = clock.Add(time.Second)
	requested, err := operator.RequestCancel(ctx, workboard.RequestCardControl{BoardID: boardID, CardID: card.ID, IdempotencyKey: "rollback-cancel-request", ExpectedCardRevision: card.Revision + 1})
	if err != nil {
		t.Fatal(err)
	}
	request := workboard.FinalizeCancelRequest{BoardID: boardID, CardID: card.ID, AttemptID: attemptID, ClaimID: claimID,
		IdempotencyKey: "rollback-cancel-final", ExpectedCardRevision: *requested.CardRevision, ExpectedClaimRevision: 1, Proof: recoveryIntent(proof)}
	clock = clock.Add(time.Second)
	if _, err = store.db.Exec(`CREATE TRIGGER fail_cancel_event BEFORE INSERT ON workboard_events WHEN NEW.kind='card.cancel_finalize' BEGIN SELECT RAISE(ABORT,'cancel event failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = operator.FinalizeCancel(ctx, request); err == nil || !strings.Contains(err.Error(), "cancel event failure") {
		t.Fatalf("rollback error=%v", err)
	}
	assertLifecycleRevisions(t, store, attemptID, claimID, 1, 1)
	if _, err = store.db.Exec(`DROP TRIGGER fail_cancel_event`); err != nil {
		t.Fatal(err)
	}
	if _, err = operator.FinalizeCancel(ctx, request); err != nil {
		t.Fatal(err)
	}
	if verifier.count() != 2 {
		t.Fatalf("verifier calls=%d", verifier.count())
	}
	changed := request
	changed.Proof.EffectResolution = workboard.ResolvedNoReplay
	if _, err = operator.FinalizeCancel(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed exact-replay error=%v", err)
	}
	workerControl := newTestControlService(t, store, workboard.Actor{ID: "worker-b", Type: "worker"}, verifier, &clock)
	if _, err = workerControl.Block(ctx, workboard.ClaimCardControl{BoardID: boardID, CardID: card.ID, AttemptID: attemptID, ClaimID: claimID,
		IdempotencyKey: "wrong-owner-block", ExpectedCardRevision: *requested.CardRevision, ExpectedClaimRevision: 1, ReasonCode: "blocked"}); err == nil {
		t.Fatal("wrong owner block accepted")
	}
}

func newTestControlService(t *testing.T, store *Store, actor workboard.Actor, verifier *controlVerifier, clock *time.Time) *workboard.ControlService {
	t.Helper()
	service, err := workboard.NewControlService(store, telemetryCardAuthority{authority: workboard.Authority{CreationScope: "session", Actor: actor}}, verifier, func() time.Time { return *clock })
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func assertCanceledControlState(t *testing.T, store *Store, boardID, cardID, attemptID, claimID string) {
	t.Helper()
	var cardState, attemptState, claimState string
	var currentClaim any
	var cancelRequested, pauseRequested int
	if err := store.db.QueryRow(`SELECT state,current_claim_id,cancel_requested,pause_requested FROM workboard_cards WHERE board_id=? AND id=?`, boardID, cardID).
		Scan(&cardState, &currentClaim, &cancelRequested, &pauseRequested); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT state FROM workboard_attempts WHERE id=?`, attemptID).Scan(&attemptState); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT state FROM workboard_claims WHERE id=?`, claimID).Scan(&claimState); err != nil {
		t.Fatal(err)
	}
	if cardState != "canceled" || currentClaim != nil || cancelRequested != 0 || pauseRequested != 0 || attemptState != "canceled" || claimState != "released" {
		t.Fatalf("card=%s current=%v flags=%d/%d attempt=%s claim=%s", cardState, currentClaim, cancelRequested, pauseRequested, attemptState, claimState)
	}
}
