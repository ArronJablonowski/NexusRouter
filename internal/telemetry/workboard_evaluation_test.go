package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

type evaluationFixture struct {
	mu       sync.Mutex
	evidence []workboard.EvidenceInput
	calls    int
}

func (e *evaluationFixture) EvaluateCandidate(context.Context, workboard.SubmitCandidateRequest, workboard.Actor) ([]workboard.EvidenceInput, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	return append([]workboard.EvidenceInput{}, e.evidence...), nil
}

func (e *evaluationFixture) count() int { e.mu.Lock(); defer e.mu.Unlock(); return e.calls }

type barrierEvaluationFixture struct {
	mu       sync.Mutex
	evidence []workboard.EvidenceInput
	calls    int
	ready    chan struct{}
	release  chan struct{}
}

func (e *barrierEvaluationFixture) EvaluateCandidate(ctx context.Context, _ workboard.SubmitCandidateRequest, _ workboard.Actor) ([]workboard.EvidenceInput, error) {
	e.mu.Lock()
	e.calls++
	if e.calls == 2 {
		close(e.ready)
	}
	e.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-e.release:
		return append([]workboard.EvidenceInput{}, e.evidence...), nil
	}
}

func (e *barrierEvaluationFixture) count() int { e.mu.Lock(); defer e.mu.Unlock(); return e.calls }

func TestWorkboardCandidateAcceptanceReplayRestartAndPrecedence(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	card, boardID := readyLifecycleCard(t, ctx, store, clock)
	worker := workboard.Actor{ID: "worker-a", Type: "worker"}
	clock = card.UpdatedAt.Add(time.Second)
	lifecycle := newTestLifecycleService(t, store, worker, verifiedLifecycleRecovery("evaluation-proof", workboard.EffectFree), &clock)
	if _, err = lifecycle.Claim(ctx, workboard.ClaimRequest{BoardID: boardID, CardID: card.ID, IdempotencyKey: "evaluation-claim01", ExpectedCardRevision: card.Revision}); err != nil {
		t.Fatal(err)
	}
	attemptID, claimID := currentLifecycleIDs(t, store, boardID, card.ID)
	evaluator := &evaluationFixture{evidence: []workboard.EvidenceInput{
		{CriterionID: "tests", Source: "model_audit", Outcome: "failed", ActorID: "audit-model", ActorType: "model", Reference: "audit-result"},
		{CriterionID: "tests", Source: "deterministic", Outcome: "passed", ActorID: "go-test", ActorType: "validator", Reference: "test-report"},
	}}
	clock = clock.Add(time.Second)
	workerService := newTestEvaluationService(t, store, worker, evaluator, &clock)
	submitRequest := workboard.SubmitCandidateRequest{BoardID: boardID, CardID: card.ID, AttemptID: attemptID, ClaimID: claimID,
		IdempotencyKey: "candidate-submit01", ExpectedCardRevision: card.Revision + 1, ExpectedClaimRevision: 1,
		CriteriaRevision: card.CriteriaRevision, Summary: "Implementation and tests", ArtifactRefs: []string{"artifact-one"}}
	submitted, err := workerService.SubmitCandidate(ctx, submitRequest)
	if err != nil || submitted.ClaimRevision == nil || *submitted.ClaimRevision != 2 || evaluator.count() != 1 {
		t.Fatalf("submit=%+v calls=%d err=%v", submitted, evaluator.count(), err)
	}
	candidate, evidence := evaluationRows(t, store, boardID, card.ID, attemptID)
	if candidate.EvidenceCount != 2 || evidence[0].Source != "model_audit" || evidence[1].Source != "deterministic" {
		t.Fatal(candidate, evidence)
	}
	current, err := store.GetCard(ctx, boardID, card.ID)
	if err != nil || current.State != workboard.Review || current.CurrentClaimID != "" {
		t.Fatal(current, err)
	}
	decision := workboard.DecideCandidateRequest{BoardID: boardID, CardID: card.ID, AttemptID: attemptID, CandidateID: candidate.ID,
		IdempotencyKey: "candidate-accept01", ExpectedCardRevision: current.Revision, CriteriaRevision: card.CriteriaRevision,
		CandidateDigest: candidate.Digest, CriteriaDigest: candidate.CriteriaDigest, EvidenceHeadRevision: int64(len(evidence)),
		EvidenceSetDigest: workboard.EvidenceSetDigest(evidence), PolicyDigest: candidate.PolicyDigest, Evidence: "objective evidence accepted"}
	self := newTestEvaluationService(t, store, workboard.Actor{ID: "worker-a", Type: "validator"}, evaluator, &clock)
	if _, err = self.AcceptCandidate(ctx, decision); !errors.Is(err, &workboard.Violation{Code: workboard.CodeInvalid}) {
		t.Fatalf("self acceptance=%v", err)
	}
	clock = clock.Add(time.Second)
	operator := newTestEvaluationService(t, store, workboard.Actor{ID: "operator-a", Type: "operator"}, evaluator, &clock)
	for name, change := range map[string]func(*workboard.DecideCandidateRequest){
		"candidate": func(r *workboard.DecideCandidateRequest) { r.CandidateDigest = digestBytes([]byte("other-candidate")) },
		"criteria":  func(r *workboard.DecideCandidateRequest) { r.CriteriaDigest = digestBytes([]byte("other-criteria")) },
		"evidence":  func(r *workboard.DecideCandidateRequest) { r.EvidenceSetDigest = digestBytes([]byte("other-evidence")) },
		"policy":    func(r *workboard.DecideCandidateRequest) { r.PolicyDigest = digestBytes([]byte("other-policy")) },
	} {
		stale := decision
		stale.IdempotencyKey = "stale-" + name + "-fence"
		change(&stale)
		if _, staleErr := operator.AcceptCandidate(ctx, stale); !errors.Is(staleErr, &workboard.Violation{Code: workboard.CodeStaleRevision}) {
			t.Fatalf("%s fence error=%v", name, staleErr)
		}
	}
	accepted, err := operator.AcceptCandidate(ctx, decision)
	if err != nil || accepted.CardRevision == nil {
		t.Fatal(accepted, err)
	}
	current, err = store.GetCard(ctx, boardID, card.ID)
	if err != nil || current.State != workboard.Done || current.AcceptanceID == "" {
		t.Fatal(current, err)
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
	workerService = newTestEvaluationService(t, store, worker, evaluator, &clock)
	replayed, err := workerService.SubmitCandidate(ctx, submitRequest)
	if err != nil || !reflect.DeepEqual(replayed, submitted) || evaluator.count() != 1 {
		t.Fatal(replayed, evaluator.count(), err)
	}
	operator = newTestEvaluationService(t, store, workboard.Actor{ID: "operator-a", Type: "operator"}, evaluator, &clock)
	replayedAccept, err := operator.AcceptCandidate(ctx, decision)
	if err != nil || !reflect.DeepEqual(replayedAccept, accepted) {
		t.Fatal(replayedAccept, err)
	}
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	acceptance, err := readEvaluationAcceptance(ctx, tx, boardID, card.ID, attemptID)
	_ = tx.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	acceptance.Rationale = "coordinated but unauthorized rewrite"
	acceptanceBody, _ := json.Marshal(acceptance)
	if _, err = store.db.Exec(`UPDATE workboard_acceptances SET body=? WHERE attempt_id=?`, acceptanceBody, attemptID); err != nil {
		t.Fatal(err)
	}
	if _, err = operator.AcceptCandidate(ctx, decision); !errors.Is(err, ErrWorkboardCorrupt) {
		t.Fatalf("acceptance tamper replay=%v", err)
	}
}

func TestWorkboardCandidateRejectAndConcurrentEvaluationOnce(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	card, boardID := readyLifecycleCard(t, ctx, store, clock)
	worker := workboard.Actor{ID: "worker-b", Type: "worker"}
	clock = card.UpdatedAt.Add(time.Second)
	lifecycle := newTestLifecycleService(t, store, worker, verifiedLifecycleRecovery("reject-proof", workboard.EffectFree), &clock)
	if _, err = lifecycle.Claim(ctx, workboard.ClaimRequest{BoardID: boardID, CardID: card.ID, IdempotencyKey: "reject-claim-key1", ExpectedCardRevision: card.Revision}); err != nil {
		t.Fatal(err)
	}
	attemptID, claimID := currentLifecycleIDs(t, store, boardID, card.ID)
	clock = clock.Add(time.Second)
	evaluator := &evaluationFixture{evidence: []workboard.EvidenceInput{{CriterionID: "tests", Source: "deterministic", Outcome: "failed", ActorID: "go-test", ActorType: "validator", Reference: "failed-tests"}}}
	service := newTestEvaluationService(t, store, worker, evaluator, &clock)
	request := workboard.SubmitCandidateRequest{BoardID: boardID, CardID: card.ID, AttemptID: attemptID, ClaimID: claimID, IdempotencyKey: "concurrent-submit", ExpectedCardRevision: card.Revision + 1, ExpectedClaimRevision: 1, CriteriaRevision: card.CriteriaRevision, Summary: "candidate"}
	const callers = 8
	results := make(chan workboard.OperationReceipt, callers)
	errs := make(chan error, callers)
	var group sync.WaitGroup
	for range callers {
		group.Add(1)
		go func() {
			defer group.Done()
			got, callErr := service.SubmitCandidate(ctx, request)
			results <- got
			errs <- callErr
		}()
	}
	group.Wait()
	close(results)
	close(errs)
	for callErr := range errs {
		if callErr != nil {
			t.Fatal(callErr)
		}
	}
	var first workboard.OperationReceipt
	for got := range results {
		if first.OperationID == "" {
			first = got
		} else if !reflect.DeepEqual(first, got) {
			t.Fatal("non-exact replay")
		}
	}
	if evaluator.count() != 1 {
		t.Fatalf("evaluator calls=%d", evaluator.count())
	}
	candidate, evidence := evaluationRows(t, store, boardID, card.ID, attemptID)
	current, _ := store.GetCard(ctx, boardID, card.ID)
	clock = clock.Add(time.Second)
	operator := newTestEvaluationService(t, store, workboard.Actor{ID: "operator-b", Type: "operator"}, evaluator, &clock)
	decision := workboard.DecideCandidateRequest{BoardID: boardID, CardID: card.ID, AttemptID: attemptID, CandidateID: candidate.ID, IdempotencyKey: "candidate-reject01",
		ExpectedCardRevision: current.Revision, CriteriaRevision: card.CriteriaRevision, CandidateDigest: candidate.Digest, CriteriaDigest: candidate.CriteriaDigest,
		EvidenceHeadRevision: 1, EvidenceSetDigest: workboard.EvidenceSetDigest(evidence), PolicyDigest: candidate.PolicyDigest, Evidence: "tests failed"}
	if _, err = operator.RejectCandidate(ctx, decision); err != nil {
		t.Fatal(err)
	}
	current, _ = store.GetCard(ctx, boardID, card.ID)
	if current.State != workboard.Ready || current.CurrentAttemptID != "" {
		t.Fatal(current)
	}
}

func TestWorkboardCandidateDistinctIdempotencyKeysDoNotCoalesce(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	card, boardID := readyLifecycleCard(t, ctx, store, clock)
	worker := workboard.Actor{ID: "worker-distinct", Type: "worker"}
	clock = card.UpdatedAt.Add(time.Second)
	lifecycle := newTestLifecycleService(t, store, worker, verifiedLifecycleRecovery("distinct-proof", workboard.EffectFree), &clock)
	if _, err = lifecycle.Claim(ctx, workboard.ClaimRequest{BoardID: boardID, CardID: card.ID, IdempotencyKey: "distinct-claim-key", ExpectedCardRevision: card.Revision}); err != nil {
		t.Fatal(err)
	}
	attemptID, claimID := currentLifecycleIDs(t, store, boardID, card.ID)
	clock = clock.Add(time.Second)
	evaluator := &barrierEvaluationFixture{ready: make(chan struct{}), release: make(chan struct{}), evidence: []workboard.EvidenceInput{
		{CriterionID: "tests", Source: "deterministic", Outcome: "passed", ActorID: "go-test", ActorType: "validator", Reference: "tests"},
	}}
	service := newTestEvaluationService(t, store, worker, evaluator, &clock)
	base := workboard.SubmitCandidateRequest{BoardID: boardID, CardID: card.ID, AttemptID: attemptID, ClaimID: claimID,
		ExpectedCardRevision: card.Revision + 1, ExpectedClaimRevision: 1, CriteriaRevision: card.CriteriaRevision, Summary: "same candidate"}
	requests := []workboard.SubmitCandidateRequest{base, base}
	requests[0].IdempotencyKey, requests[1].IdempotencyKey = "distinct-submit-key-a", "distinct-submit-key-b"
	errs := make(chan error, 2)
	for _, request := range requests {
		go func() {
			_, callErr := service.SubmitCandidate(ctx, request)
			errs <- callErr
		}()
	}
	select {
	case <-evaluator.ready:
	case <-time.After(5 * time.Second):
		t.Fatal("distinct idempotency keys were incorrectly coalesced")
	}
	close(evaluator.release)
	successes := 0
	for range 2 {
		if callErr := <-errs; callErr == nil {
			successes++
		}
	}
	if evaluator.count() != 2 || successes != 1 {
		t.Fatalf("evaluator calls=%d successes=%d", evaluator.count(), successes)
	}
}

func TestWorkboardSubjectiveFeedbackIsAddedDuringReview(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	card, boardID := readyLifecycleCard(t, ctx, store, clock)
	criteria := []workboard.AcceptanceCriterion{
		{Version: 1, ID: "tests", Kind: "objective", RequiredSource: "deterministic", ValidatorID: "go-test", Description: "Tests pass.", Required: true},
		{Version: 1, ID: "creative", Kind: "subjective", RequiredSource: "user_feedback", ValidatorID: "operator", Description: "User approves.", Required: true},
	}
	clock = card.UpdatedAt.Add(time.Second)
	progress := newTestProgressService(t, store, workboard.Actor{ID: "operator", Type: "operator"}, &clock)
	if _, err = progress.ReviseCriteria(ctx, workboard.ReviseCriteriaRequest{BoardID: boardID, CardID: card.ID, IdempotencyKey: "subjective-criteria", ExpectedCardRevision: card.Revision, ExpectedCriteriaRevision: card.CriteriaRevision, Criteria: criteria}); err != nil {
		t.Fatal(err)
	}
	card, err = store.GetCard(ctx, boardID, card.ID)
	if err != nil {
		t.Fatal(err)
	}
	worker := workboard.Actor{ID: "worker-subjective", Type: "worker"}
	clock = clock.Add(time.Second)
	lifecycle := newTestLifecycleService(t, store, worker, verifiedLifecycleRecovery("subjective-proof", workboard.EffectFree), &clock)
	if _, err = lifecycle.Claim(ctx, workboard.ClaimRequest{BoardID: boardID, CardID: card.ID, IdempotencyKey: "subjective-claim", ExpectedCardRevision: card.Revision}); err != nil {
		t.Fatal(err)
	}
	attemptID, claimID := currentLifecycleIDs(t, store, boardID, card.ID)
	clock = clock.Add(time.Second)
	evaluator := &evaluationFixture{evidence: []workboard.EvidenceInput{
		{CriterionID: "tests", Source: "deterministic", Outcome: "passed", ActorID: "go-test", ActorType: "validator", Reference: "tests"},
		{CriterionID: "creative", Source: "model_audit", Outcome: "failed", ActorID: "audit-model", ActorType: "model", Reference: "advisory"},
	}}
	workerService := newTestEvaluationService(t, store, worker, evaluator, &clock)
	submit := workboard.SubmitCandidateRequest{BoardID: boardID, CardID: card.ID, AttemptID: attemptID, ClaimID: claimID, IdempotencyKey: "subjective-submit",
		ExpectedCardRevision: card.Revision + 1, ExpectedClaimRevision: 1, CriteriaRevision: card.CriteriaRevision, Summary: "creative candidate"}
	if _, err = workerService.SubmitCandidate(ctx, submit); err != nil {
		t.Fatal(err)
	}
	candidate, prior := evaluationRows(t, store, boardID, card.ID, attemptID)
	if len(prior) != 2 {
		t.Fatalf("pre-review evidence=%d", len(prior))
	}
	current, _ := store.GetCard(ctx, boardID, card.ID)
	clock = clock.Add(time.Second)
	operator := newTestEvaluationService(t, store, workboard.Actor{ID: "operator-reviewer", Type: "operator"}, evaluator, &clock)
	decision := workboard.DecideCandidateRequest{BoardID: boardID, CardID: card.ID, AttemptID: attemptID, CandidateID: candidate.ID,
		IdempotencyKey: "subjective-accept", ExpectedCardRevision: current.Revision, CriteriaRevision: card.CriteriaRevision,
		CandidateDigest: candidate.Digest, CriteriaDigest: candidate.CriteriaDigest, EvidenceHeadRevision: int64(len(prior)),
		EvidenceSetDigest: workboard.EvidenceSetDigest(prior), PolicyDigest: candidate.PolicyDigest, Evidence: "The user approves this creative result."}
	accepted, err := operator.AcceptCandidate(ctx, decision)
	if err != nil {
		t.Fatal(err)
	}
	_, finalEvidence := evaluationRows(t, store, boardID, card.ID, attemptID)
	if len(finalEvidence) != 3 || finalEvidence[2].Source != "user_feedback" || finalEvidence[2].Outcome != "passed" || finalEvidence[2].ActorID != "operator-reviewer" {
		t.Fatalf("final evidence=%+v", finalEvidence)
	}
	tx, _ := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	acceptance, readErr := readEvaluationAcceptance(ctx, tx, boardID, card.ID, attemptID)
	_ = tx.Rollback()
	if readErr != nil || acceptance.Rationale != decision.Evidence || acceptance.PriorEvidenceHeadRevision != int64(len(prior)) ||
		acceptance.EvidenceHeadRevision != int64(len(finalEvidence)) || acceptance.EvidenceSetDigest != workboard.EvidenceSetDigest(finalEvidence) {
		t.Fatalf("acceptance=%+v err=%v", acceptance, readErr)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	operator = newTestEvaluationService(t, store, workboard.Actor{ID: "operator-reviewer", Type: "operator"}, evaluator, &clock)
	if replayed, replayErr := operator.AcceptCandidate(ctx, decision); replayErr != nil || !reflect.DeepEqual(replayed, accepted) {
		t.Fatalf("replay=%+v want=%+v err=%v", replayed, accepted, replayErr)
	}
}

func TestWorkboardSubjectiveRejectionAddsNegativeUserFeedback(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	card, boardID := readyLifecycleCard(t, ctx, store, clock)
	criteria := []workboard.AcceptanceCriterion{
		{Version: 1, ID: "tests", Kind: "objective", RequiredSource: "deterministic", ValidatorID: "go-test", Description: "Tests pass.", Required: true},
		{Version: 1, ID: "creative", Kind: "subjective", RequiredSource: "user_feedback", ValidatorID: "operator", Description: "User approves.", Required: true},
	}
	clock = card.UpdatedAt.Add(time.Second)
	progress := newTestProgressService(t, store, workboard.Actor{ID: "operator", Type: "operator"}, &clock)
	if _, err = progress.ReviseCriteria(ctx, workboard.ReviseCriteriaRequest{BoardID: boardID, CardID: card.ID, IdempotencyKey: "reject-subjective-criteria", ExpectedCardRevision: card.Revision, ExpectedCriteriaRevision: card.CriteriaRevision, Criteria: criteria}); err != nil {
		t.Fatal(err)
	}
	card, _ = store.GetCard(ctx, boardID, card.ID)
	worker := workboard.Actor{ID: "worker-subjective-reject", Type: "worker"}
	clock = clock.Add(time.Second)
	lifecycle := newTestLifecycleService(t, store, worker, verifiedLifecycleRecovery("subjective-reject-proof", workboard.EffectFree), &clock)
	if _, err = lifecycle.Claim(ctx, workboard.ClaimRequest{BoardID: boardID, CardID: card.ID, IdempotencyKey: "subjective-reject-claim", ExpectedCardRevision: card.Revision}); err != nil {
		t.Fatal(err)
	}
	attemptID, claimID := currentLifecycleIDs(t, store, boardID, card.ID)
	clock = clock.Add(time.Second)
	evaluator := &evaluationFixture{evidence: []workboard.EvidenceInput{
		{CriterionID: "tests", Source: "deterministic", Outcome: "passed", ActorID: "go-test", ActorType: "validator", Reference: "tests"},
		{CriterionID: "creative", Source: "model_audit", Outcome: "passed", ActorID: "audit-model", ActorType: "model", Reference: "advisory"},
	}}
	workerService := newTestEvaluationService(t, store, worker, evaluator, &clock)
	submit := workboard.SubmitCandidateRequest{BoardID: boardID, CardID: card.ID, AttemptID: attemptID, ClaimID: claimID, IdempotencyKey: "subjective-reject-submit",
		ExpectedCardRevision: card.Revision + 1, ExpectedClaimRevision: 1, CriteriaRevision: card.CriteriaRevision, Summary: "creative candidate"}
	if _, err = workerService.SubmitCandidate(ctx, submit); err != nil {
		t.Fatal(err)
	}
	candidate, prior := evaluationRows(t, store, boardID, card.ID, attemptID)
	current, _ := store.GetCard(ctx, boardID, card.ID)
	clock = clock.Add(time.Second)
	operator := newTestEvaluationService(t, store, workboard.Actor{ID: "operator-rejecter", Type: "operator"}, evaluator, &clock)
	decision := workboard.DecideCandidateRequest{BoardID: boardID, CardID: card.ID, AttemptID: attemptID, CandidateID: candidate.ID,
		IdempotencyKey: "subjective-reject", ExpectedCardRevision: current.Revision, CriteriaRevision: card.CriteriaRevision,
		CandidateDigest: candidate.Digest, CriteriaDigest: candidate.CriteriaDigest, EvidenceHeadRevision: int64(len(prior)),
		EvidenceSetDigest: workboard.EvidenceSetDigest(prior), PolicyDigest: candidate.PolicyDigest, Evidence: "The user rejects this creative result."}
	if _, err = operator.RejectCandidate(ctx, decision); err != nil {
		t.Fatal(err)
	}
	_, finalEvidence := evaluationRows(t, store, boardID, card.ID, attemptID)
	current, _ = store.GetCard(ctx, boardID, card.ID)
	if current.State != workboard.Ready || len(finalEvidence) != 3 || finalEvidence[2].Source != "user_feedback" || finalEvidence[2].Outcome != "failed" {
		t.Fatalf("card=%+v evidence=%+v", current, finalEvidence)
	}
}

func TestWorkboardEvaluationTamperFailsClosed(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	card, boardID := readyLifecycleCard(t, ctx, store, clock)
	worker := workboard.Actor{ID: "worker-c", Type: "worker"}
	clock = card.UpdatedAt.Add(time.Second)
	lifecycle := newTestLifecycleService(t, store, worker, verifiedLifecycleRecovery("tamper-proof", workboard.EffectFree), &clock)
	if _, err = lifecycle.Claim(ctx, workboard.ClaimRequest{BoardID: boardID, CardID: card.ID, IdempotencyKey: "tamper-claim-key", ExpectedCardRevision: card.Revision}); err != nil {
		t.Fatal(err)
	}
	attemptID, claimID := currentLifecycleIDs(t, store, boardID, card.ID)
	clock = clock.Add(time.Second)
	evaluator := &evaluationFixture{evidence: []workboard.EvidenceInput{{CriterionID: "tests", Source: "deterministic", Outcome: "passed", ActorID: "go-test", ActorType: "validator", Reference: "tests"}}}
	service := newTestEvaluationService(t, store, worker, evaluator, &clock)
	request := workboard.SubmitCandidateRequest{BoardID: boardID, CardID: card.ID, AttemptID: attemptID, ClaimID: claimID, IdempotencyKey: "tamper-submit-key", ExpectedCardRevision: card.Revision + 1, ExpectedClaimRevision: 1, CriteriaRevision: card.CriteriaRevision, Summary: "candidate"}
	if _, err = store.db.Exec(`CREATE TRIGGER fail_candidate_event BEFORE INSERT ON workboard_events WHEN NEW.kind='candidate.submit' BEGIN SELECT RAISE(ABORT,'candidate event failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = service.SubmitCandidate(ctx, request); err == nil {
		t.Fatal("candidate transaction did not roll back")
	}
	for _, table := range []string{"workboard_candidates", "workboard_evidence", "workboard_acceptances"} {
		var count int
		if err = store.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s count=%d err=%v", table, count, err)
		}
	}
	if _, err = store.db.Exec(`DROP TRIGGER fail_candidate_event`); err != nil {
		t.Fatal(err)
	}
	if _, err = service.SubmitCandidate(ctx, request); err != nil {
		t.Fatal(err)
	}
	candidate, evidence := evaluationRows(t, store, boardID, card.ID, attemptID)
	evidence[0].Reference = "coordinated-tamper"
	candidate.EvidenceDigest = workboard.EvidenceSetDigest(evidence)
	candidateBody, _ := json.Marshal(candidate)
	evidenceBody, _ := json.Marshal(evidence[0])
	if _, err = store.db.Exec(`UPDATE workboard_candidates SET evidence_digest=?,body=? WHERE attempt_id=?`, candidate.EvidenceDigest, candidateBody, attemptID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`UPDATE workboard_evidence SET reference=?,body=? WHERE attempt_id=? AND revision=1`, evidence[0].Reference, evidenceBody, attemptID); err != nil {
		t.Fatal(err)
	}
	if _, err = service.SubmitCandidate(ctx, request); !errors.Is(err, ErrWorkboardCorrupt) {
		t.Fatalf("tamper replay=%v", err)
	}
}

func newTestEvaluationService(t *testing.T, store *Store, actor workboard.Actor, evaluator workboard.CandidateEvaluator, clock *time.Time) *workboard.EvaluationService {
	t.Helper()
	service, err := workboard.NewEvaluationService(store, telemetryCardAuthority{authority: workboard.Authority{CreationScope: "acceptance-authority", Actor: actor}}, evaluator, func() time.Time { return *clock })
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func evaluationRows(t *testing.T, store *Store, boardID, cardID, attemptID string) (workboard.CandidateRecord, []workboard.EvidenceRecord) {
	t.Helper()
	tx, err := store.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	candidate, err := readEvaluationCandidate(context.Background(), tx, boardID, cardID, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := readEvaluationEvidence(context.Background(), tx, boardID, cardID, attemptID, candidate)
	if err != nil {
		t.Fatal(err)
	}
	return candidate, evidence
}
