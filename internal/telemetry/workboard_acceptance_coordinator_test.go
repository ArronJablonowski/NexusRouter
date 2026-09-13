package telemetry

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

func TestAcceptanceCoordinatorPersistsCriterionBoundDecision(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "coordinator.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	card, boardID := readyLifecycleCard(t, ctx, store, clock)
	worker := workboard.Actor{ID: "worker-a", Type: "worker"}
	clock = card.UpdatedAt.Add(time.Second)
	lifecycle := newTestLifecycleService(t, store, worker, verifiedLifecycleRecovery("coordinator-proof", workboard.EffectFree), &clock)
	if _, err = lifecycle.Claim(ctx, workboard.ClaimRequest{BoardID: boardID, CardID: card.ID,
		IdempotencyKey: "coordinator-claim", ExpectedCardRevision: card.Revision}); err != nil {
		t.Fatal(err)
	}
	attemptID, claimID := currentLifecycleIDs(t, store, boardID, card.ID)
	evaluator := &evaluationFixture{evidence: []workboard.EvidenceInput{
		{CriterionID: "tests", Source: "model_audit", Outcome: "failed", ActorID: "hostile-model", ActorType: "model", Reference: "audit-ref"},
		{CriterionID: "tests", Source: "deterministic", Outcome: "passed", ActorID: "go-test", ActorType: "validator", Reference: "test-ref"},
	}}
	clock = clock.Add(time.Second)
	workerService := newTestEvaluationService(t, store, worker, evaluator, &clock)
	if _, err = workerService.SubmitCandidate(ctx, workboard.SubmitCandidateRequest{BoardID: boardID, CardID: card.ID,
		AttemptID: attemptID, ClaimID: claimID, IdempotencyKey: "coordinator-candidate", ExpectedCardRevision: card.Revision + 1,
		ExpectedClaimRevision: 1, CriteriaRevision: card.CriteriaRevision, Summary: "Implemented and tested."}); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Second)
	validator := newTestEvaluationService(t, store, workboard.Actor{ID: "criterion-coordinator", Type: "validator"}, evaluator, &clock)
	coordinator, err := workboard.NewAcceptanceCoordinator(store, validator)
	if err != nil {
		t.Fatal(err)
	}
	result, err := coordinator.Coordinate(ctx, boardID, card.ID)
	if err != nil || result.Decision != workboard.DecisionAccepted || result.Receipt == nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	snapshots, err := store.ReadCardLifecycleSnapshots(ctx, boardID, []string{card.ID})
	attempt := snapshots[card.ID].Attempt
	if err != nil || attempt == nil || attempt.State != "accepted" || attempt.Acceptance == nil ||
		attempt.Acceptance.DecidedByType != "validator" || attempt.Acceptance.DecidedBy != "criterion-coordinator" ||
		!strings.Contains(attempt.Acceptance.Rationale, "test-ref") || strings.Contains(attempt.Acceptance.Rationale, "audit-ref") {
		t.Fatalf("attempt=%+v err=%v", attempt, err)
	}
}

func TestAcceptanceCoordinatorRejectsRequiredDeterministicFailure(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "coordinator-reject.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := time.Date(2026, 9, 13, 12, 30, 0, 0, time.UTC)
	card, boardID := readyLifecycleCard(t, ctx, store, clock)
	worker := workboard.Actor{ID: "worker-reject", Type: "worker"}
	clock = card.UpdatedAt.Add(time.Second)
	lifecycle := newTestLifecycleService(t, store, worker, verifiedLifecycleRecovery("coordinator-reject-proof", workboard.EffectFree), &clock)
	if _, err = lifecycle.Claim(ctx, workboard.ClaimRequest{BoardID: boardID, CardID: card.ID,
		IdempotencyKey: "coordinator-reject-claim", ExpectedCardRevision: card.Revision}); err != nil {
		t.Fatal(err)
	}
	attemptID, claimID := currentLifecycleIDs(t, store, boardID, card.ID)
	evaluator := &evaluationFixture{evidence: []workboard.EvidenceInput{
		{CriterionID: "tests", Source: "deterministic", Outcome: "failed", ActorID: "go-test", ActorType: "validator", Reference: "failed-test-ref"},
		{CriterionID: "tests", Source: "model_audit", Outcome: "passed", ActorID: "optimistic-model", ActorType: "model", Reference: "optimistic-audit-ref"},
	}}
	clock = clock.Add(time.Second)
	workerService := newTestEvaluationService(t, store, worker, evaluator, &clock)
	if _, err = workerService.SubmitCandidate(ctx, workboard.SubmitCandidateRequest{BoardID: boardID, CardID: card.ID,
		AttemptID: attemptID, ClaimID: claimID, IdempotencyKey: "coordinator-reject-candidate", ExpectedCardRevision: card.Revision + 1,
		ExpectedClaimRevision: 1, CriteriaRevision: card.CriteriaRevision, Summary: "Candidate with failing tests."}); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Second)
	validator := newTestEvaluationService(t, store, workboard.Actor{ID: "criterion-coordinator", Type: "validator"}, evaluator, &clock)
	coordinator, err := workboard.NewAcceptanceCoordinator(store, validator)
	if err != nil {
		t.Fatal(err)
	}
	result, err := coordinator.Coordinate(ctx, boardID, card.ID)
	if err != nil || result.Decision != workboard.DecisionRejected || result.Receipt == nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	card, err = store.GetCard(ctx, boardID, card.ID)
	snapshots, snapshotErr := store.ReadCardLifecycleSnapshots(ctx, boardID, []string{card.ID})
	attempt := snapshots[card.ID].Attempt
	if err != nil || snapshotErr != nil || card.State != workboard.Ready || attempt == nil || attempt.State != "rejected" ||
		attempt.Acceptance == nil || attempt.Acceptance.Decision != "rejected" ||
		!strings.Contains(attempt.Acceptance.Rationale, "failed-test-ref") || strings.Contains(attempt.Acceptance.Rationale, "optimistic-audit-ref") {
		t.Fatalf("card=%+v attempt=%+v errors=%v/%v", card, attempt, err, snapshotErr)
	}
}

type gatedDecisionWriter struct {
	delegate *workboard.EvaluationService
	entered  chan struct{}
	release  chan struct{}
}

func (w gatedDecisionWriter) AcceptCandidate(ctx context.Context, request workboard.DecideCandidateRequest) (workboard.OperationReceipt, error) {
	close(w.entered)
	select {
	case <-ctx.Done():
		return workboard.OperationReceipt{}, ctx.Err()
	case <-w.release:
		return w.delegate.AcceptCandidate(ctx, request)
	}
}

func (w gatedDecisionWriter) RejectCandidate(ctx context.Context, request workboard.DecideCandidateRequest) (workboard.OperationReceipt, error) {
	return w.delegate.RejectCandidate(ctx, request)
}

func TestAcceptanceCoordinatorPreservesConcurrentOperatorDecision(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "coordinator-race.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := time.Date(2026, 9, 13, 13, 0, 0, 0, time.UTC)
	card, boardID := readyLifecycleCard(t, ctx, store, clock)
	worker := workboard.Actor{ID: "worker-race", Type: "worker"}
	clock = card.UpdatedAt.Add(time.Second)
	lifecycle := newTestLifecycleService(t, store, worker, verifiedLifecycleRecovery("race-recovery-proof", workboard.EffectFree), &clock)
	if _, err = lifecycle.Claim(ctx, workboard.ClaimRequest{BoardID: boardID, CardID: card.ID,
		IdempotencyKey: "race-claim-operation", ExpectedCardRevision: card.Revision}); err != nil {
		t.Fatal(err)
	}
	attemptID, claimID := currentLifecycleIDs(t, store, boardID, card.ID)
	evaluator := &evaluationFixture{evidence: []workboard.EvidenceInput{{CriterionID: "tests", Source: "deterministic",
		Outcome: "passed", ActorID: "go-test", ActorType: "validator", Reference: "race-test-ref"}}}
	clock = clock.Add(time.Second)
	workerService := newTestEvaluationService(t, store, worker, evaluator, &clock)
	if _, err = workerService.SubmitCandidate(ctx, workboard.SubmitCandidateRequest{BoardID: boardID, CardID: card.ID,
		AttemptID: attemptID, ClaimID: claimID, IdempotencyKey: "race-candidate-operation", ExpectedCardRevision: card.Revision + 1,
		ExpectedClaimRevision: 1, CriteriaRevision: card.CriteriaRevision, Summary: "Completed."}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.ReadAcceptanceDecision(ctx, boardID, card.ID)
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Second)
	validator := newTestEvaluationService(t, store, workboard.Actor{ID: "coordinator-race", Type: "validator"}, evaluator, &clock)
	gate := gatedDecisionWriter{delegate: validator, entered: make(chan struct{}), release: make(chan struct{})}
	coordinator, err := workboard.NewAcceptanceCoordinator(store, gate)
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		result workboard.AcceptanceCoordinationResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, coordinateErr := coordinator.Coordinate(ctx, boardID, card.ID)
		done <- outcome{result: result, err: coordinateErr}
	}()
	<-gate.entered
	candidate := snapshot.Attempt.Candidate
	operator := newTestEvaluationService(t, store, workboard.Actor{ID: "operator-race", Type: "operator"}, evaluator, &clock)
	_, err = operator.AcceptCandidate(ctx, workboard.DecideCandidateRequest{BoardID: boardID, CardID: card.ID,
		AttemptID: snapshot.Attempt.ID, CandidateID: candidate.ID, IdempotencyKey: "operator-race-decision",
		ExpectedCardRevision: snapshot.CardRevision, CriteriaRevision: snapshot.Attempt.CriteriaRevision,
		EvidenceHeadRevision: int64(len(snapshot.Attempt.Evidence)), CandidateDigest: candidate.Digest,
		CriteriaDigest: snapshot.Attempt.CriteriaDigest, EvidenceSetDigest: workboard.EvidenceSetDigest(snapshot.Attempt.Evidence),
		PolicyDigest: snapshot.Attempt.PolicyDigest, Evidence: "operator independently accepted objective evidence"})
	if err != nil {
		t.Fatal(err)
	}
	close(gate.release)
	coordinated := <-done
	if coordinated.err != nil || coordinated.result.Decision != workboard.DecisionRaced {
		t.Fatalf("result=%+v err=%v", coordinated.result, coordinated.err)
	}
	final, err := store.ReadAcceptanceDecision(ctx, boardID, card.ID)
	if err != nil || final.Attempt.Acceptance == nil || final.Attempt.Acceptance.DecidedBy != "operator-race" {
		t.Fatalf("final=%+v err=%v", final, err)
	}
}
