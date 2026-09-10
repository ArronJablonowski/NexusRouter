package telemetry

import (
	"context"
	"database/sql"
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

func TestWorkboardSubjectiveOnlyCandidateStartsWithEmptyEvidence(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	card, boardID := readyLifecycleCard(t, ctx, store, clock)
	criteria := []workboard.AcceptanceCriterion{{Version: 1, ID: "creative", Kind: "subjective", RequiredSource: "user_feedback",
		ValidatorID: "operator", Description: "The user approves the creative result.", Required: true}}
	clock = card.UpdatedAt.Add(time.Second)
	progress := newTestProgressService(t, store, workboard.Actor{ID: "operator", Type: "operator"}, &clock)
	if _, err = progress.ReviseCriteria(ctx, workboard.ReviseCriteriaRequest{BoardID: boardID, CardID: card.ID,
		IdempotencyKey: "subjective-only-criteria", ExpectedCardRevision: card.Revision,
		ExpectedCriteriaRevision: card.CriteriaRevision, Criteria: criteria}); err != nil {
		t.Fatal(err)
	}
	card, err = store.GetCard(ctx, boardID, card.ID)
	if err != nil {
		t.Fatal(err)
	}
	worker := workboard.Actor{ID: "worker-subjective-only", Type: "worker"}
	clock = clock.Add(time.Second)
	lifecycle := newTestLifecycleService(t, store, worker, verifiedLifecycleRecovery("subjective-only-proof", workboard.EffectFree), &clock)
	if _, err = lifecycle.Claim(ctx, workboard.ClaimRequest{BoardID: boardID, CardID: card.ID,
		IdempotencyKey: "subjective-only-claim", ExpectedCardRevision: card.Revision}); err != nil {
		t.Fatal(err)
	}
	attemptID, claimID := currentLifecycleIDs(t, store, boardID, card.ID)
	clock = clock.Add(time.Second)
	evaluator := &evaluationFixture{evidence: []workboard.EvidenceInput{}}
	workerService := newTestEvaluationService(t, store, worker, evaluator, &clock)
	submit := workboard.SubmitCandidateRequest{BoardID: boardID, CardID: card.ID, AttemptID: attemptID, ClaimID: claimID,
		IdempotencyKey: "subjective-only-submit", ExpectedCardRevision: card.Revision + 1, ExpectedClaimRevision: 1,
		CriteriaRevision: card.CriteriaRevision, Summary: "A creative candidate awaiting the user's taste judgment."}
	submitted, err := workerService.SubmitCandidate(ctx, submit)
	if err != nil || submitted.Validate() != nil || evaluator.count() != 1 {
		t.Fatalf("submit=%+v calls=%d err=%v", submitted, evaluator.count(), err)
	}
	candidate, prior := evaluationRows(t, store, boardID, card.ID, attemptID)
	if candidate.EvidenceCount != 0 || len(prior) != 0 || candidate.EvidenceDigest != workboard.EvidenceSetDigest([]workboard.EvidenceRecord{}) {
		t.Fatalf("candidate=%+v evidence=%+v", candidate, prior)
	}
	current, err := store.GetCard(ctx, boardID, card.ID)
	if err != nil || current.State != workboard.Review {
		t.Fatal(current, err)
	}
	clock = clock.Add(time.Second)
	operator := newTestEvaluationService(t, store, workboard.Actor{ID: "operator-subjective-only", Type: "operator"}, evaluator, &clock)
	decision := workboard.DecideCandidateRequest{BoardID: boardID, CardID: card.ID, AttemptID: attemptID, CandidateID: candidate.ID,
		IdempotencyKey: "subjective-only-accept", ExpectedCardRevision: current.Revision, CriteriaRevision: card.CriteriaRevision,
		CandidateDigest: candidate.Digest, CriteriaDigest: candidate.CriteriaDigest, EvidenceHeadRevision: 0,
		EvidenceSetDigest: workboard.EvidenceSetDigest(prior), PolicyDigest: candidate.PolicyDigest,
		Evidence: "The authenticated user approves this creative result."}
	accepted, err := operator.AcceptCandidate(ctx, decision)
	if err != nil || accepted.Validate() != nil {
		t.Fatalf("accept=%+v err=%v", accepted, err)
	}
	_, finalEvidence := evaluationRows(t, store, boardID, card.ID, attemptID)
	if len(finalEvidence) != 1 || finalEvidence[0].Revision != 1 || finalEvidence[0].CriterionID != "creative" ||
		finalEvidence[0].Source != "user_feedback" || finalEvidence[0].Outcome != "passed" || finalEvidence[0].ActorID != "operator-subjective-only" {
		t.Fatalf("final evidence=%+v", finalEvidence)
	}
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	acceptance, readErr := readEvaluationAcceptance(ctx, tx, boardID, card.ID, attemptID)
	_ = tx.Rollback()
	if readErr != nil || acceptance.PriorEvidenceHeadRevision != 0 ||
		acceptance.PriorEvidenceSetDigest != workboard.EvidenceSetDigest(prior) || acceptance.EvidenceHeadRevision != 1 ||
		acceptance.EvidenceSetDigest != workboard.EvidenceSetDigest(finalEvidence) {
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
	workerService = newTestEvaluationService(t, store, worker, evaluator, &clock)
	if replayed, replayErr := workerService.SubmitCandidate(ctx, submit); replayErr != nil || !reflect.DeepEqual(replayed, submitted) || evaluator.count() != 1 {
		t.Fatalf("submit replay=%+v calls=%d err=%v", replayed, evaluator.count(), replayErr)
	}
	operator = newTestEvaluationService(t, store, workboard.Actor{ID: "operator-subjective-only", Type: "operator"}, evaluator, &clock)
	if replayed, replayErr := operator.AcceptCandidate(ctx, decision); replayErr != nil || !reflect.DeepEqual(replayed, accepted) {
		t.Fatalf("accept replay=%+v want=%+v err=%v", replayed, accepted, replayErr)
	}
}

func TestWorkboardCandidateEvaluatorCannotForgeUserFeedback(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	card, boardID := readyLifecycleCard(t, ctx, store, clock)
	worker := workboard.Actor{ID: "worker-forged-feedback", Type: "worker"}
	clock = card.UpdatedAt.Add(time.Second)
	lifecycle := newTestLifecycleService(t, store, worker, verifiedLifecycleRecovery("forged-feedback-proof", workboard.EffectFree), &clock)
	if _, err = lifecycle.Claim(ctx, workboard.ClaimRequest{BoardID: boardID, CardID: card.ID,
		IdempotencyKey: "forged-feedback-claim", ExpectedCardRevision: card.Revision}); err != nil {
		t.Fatal(err)
	}
	attemptID, claimID := currentLifecycleIDs(t, store, boardID, card.ID)
	evaluator := &evaluationFixture{evidence: []workboard.EvidenceInput{{CriterionID: "tests", Source: "user_feedback",
		Outcome: "passed", ActorID: "operator-forged", ActorType: "operator", Reference: "forged-feedback"}}}
	clock = clock.Add(time.Second)
	service := newTestEvaluationService(t, store, worker, evaluator, &clock)
	request := workboard.SubmitCandidateRequest{BoardID: boardID, CardID: card.ID, AttemptID: attemptID, ClaimID: claimID,
		IdempotencyKey: "forged-feedback-submit", ExpectedCardRevision: card.Revision + 1, ExpectedClaimRevision: 1,
		CriteriaRevision: card.CriteriaRevision, Summary: "candidate with forged feedback"}
	if _, err = service.SubmitCandidate(ctx, request); !errors.Is(err, &workboard.Violation{Code: workboard.CodeInvalid}) {
		t.Fatalf("forged user feedback accepted: %v", err)
	}
	var candidates, evidence, operations int
	if err = store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workboard_candidates WHERE attempt_id=?`, attemptID).Scan(&candidates); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workboard_evidence WHERE attempt_id=?`, attemptID).Scan(&evidence); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workboard_operations WHERE operation_id=?`, request.IdempotencyKey).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	snapshots, err := store.ReadCardLifecycleSnapshots(ctx, boardID, []string{card.ID})
	snapshot := snapshots[card.ID]
	if err != nil || candidates != 0 || evidence != 0 || operations != 0 || snapshot.Attempt == nil ||
		snapshot.Attempt.Candidate != nil || snapshot.Attempt.Claim == nil || snapshot.Attempt.Claim.State != "active" ||
		snapshot.Attempt.State != "running" {
		t.Fatalf("candidate=%d evidence=%d operations=%d lifecycle=%+v err=%v", candidates, evidence, operations, snapshot, err)
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

func TestWorkboardAcceptanceUnlocksSuccessorsReplayAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	card, boardID := readyLifecycleCard(t, ctx, store, clock)
	cardService, err := workboard.NewCardService(store, telemetryCardAuthority{authority: workboard.Authority{CreationScope: "session", Actor: workboard.Actor{ID: "operator", Type: "operator"}}})
	if err != nil {
		t.Fatal(err)
	}
	first := createCardForTest(t, ctx, cardService, boardID, "successor-first-key", "First successor", 3, 2, []string{card.ID})
	second := createCardForTest(t, ctx, cardService, boardID, "successor-second-key", "Second successor", 4, 3, []string{card.ID})
	blocker := createCardForTest(t, ctx, cardService, boardID, "successor-blocker-key", "Other prerequisite", 5, 4, nil)
	partial := createCardForTest(t, ctx, cardService, boardID, "successor-partial-key", "Partially unblocked", 6, 5, []string{card.ID, blocker.ID})
	worker := workboard.Actor{ID: "worker-unlock", Type: "worker"}
	clock = card.UpdatedAt.Add(time.Second)
	lifecycle := newTestLifecycleService(t, store, worker, verifiedLifecycleRecovery("unlock-proof", workboard.EffectFree), &clock)
	if _, err = lifecycle.Claim(ctx, workboard.ClaimRequest{BoardID: boardID, CardID: card.ID, IdempotencyKey: "unlock-claim-key", ExpectedCardRevision: card.Revision}); err != nil {
		t.Fatal(err)
	}
	attemptID, claimID := currentLifecycleIDs(t, store, boardID, card.ID)
	clock = clock.Add(time.Second)
	evaluator := &evaluationFixture{evidence: []workboard.EvidenceInput{{CriterionID: "tests", Source: "deterministic", Outcome: "passed", ActorID: "go-test", ActorType: "validator", Reference: "unlock-tests"}}}
	workerService := newTestEvaluationService(t, store, worker, evaluator, &clock)
	submit := workboard.SubmitCandidateRequest{BoardID: boardID, CardID: card.ID, AttemptID: attemptID, ClaimID: claimID,
		IdempotencyKey: "unlock-submit-key", ExpectedCardRevision: card.Revision + 1, ExpectedClaimRevision: 1, CriteriaRevision: card.CriteriaRevision, Summary: "unlock candidate"}
	if _, err = workerService.SubmitCandidate(ctx, submit); err != nil {
		t.Fatal(err)
	}
	candidate, evidence := evaluationRows(t, store, boardID, card.ID, attemptID)
	current, _ := store.GetCard(ctx, boardID, card.ID)
	clock = clock.Add(time.Second)
	operator := newTestEvaluationService(t, store, workboard.Actor{ID: "operator-unlock", Type: "operator"}, evaluator, &clock)
	decision := workboard.DecideCandidateRequest{BoardID: boardID, CardID: card.ID, AttemptID: attemptID, CandidateID: candidate.ID,
		IdempotencyKey: "unlock-accept-key", ExpectedCardRevision: current.Revision, CriteriaRevision: card.CriteriaRevision,
		CandidateDigest: candidate.Digest, CriteriaDigest: candidate.CriteriaDigest, EvidenceHeadRevision: int64(len(evidence)),
		EvidenceSetDigest: workboard.EvidenceSetDigest(evidence), PolicyDigest: candidate.PolicyDigest, Evidence: "successors may proceed"}
	accepted, err := operator.AcceptCandidate(ctx, decision)
	if err != nil || accepted.EventCount != 4 || accepted.LastSequence-accepted.FirstSequence != 3 {
		t.Fatalf("accepted=%+v err=%v", accepted, err)
	}

	got := make([]workboard.Card, 0, 2)
	for _, successor := range []workboard.Card{first, second} {
		updated, readErr := store.GetCard(ctx, boardID, successor.ID)
		if readErr != nil || updated.State != workboard.Ready || updated.RemainingDependencies != 0 || updated.Revision != successor.Revision+1 {
			t.Fatalf("successor=%+v err=%v", updated, readErr)
		}
		got = append(got, updated)
	}
	if got[0].Rank == got[1].Rank {
		t.Fatalf("successor ranks collided: %q", got[0].Rank)
	}
	partiallyUpdated, readErr := store.GetCard(ctx, boardID, partial.ID)
	if readErr != nil || partiallyUpdated.State != workboard.Backlog || partiallyUpdated.RemainingDependencies != 1 || partiallyUpdated.Revision != partial.Revision+1 {
		t.Fatalf("partially updated successor=%+v err=%v", partiallyUpdated, readErr)
	}
	partialEvent, eventErr := scanCanonicalWorkboardEvent(store.db.QueryRowContext(ctx, `SELECT id,sequence,operation_id,kind,actor_id,actor_type,card_id,created_at,body
		FROM workboard_events WHERE board_id=? AND operation_id=? AND card_id=?`, boardID, accepted.OperationID, partial.ID), boardID)
	wantPartialEventID, idErr := successorEffectEventID(accepted.OperationID, card.ID, partiallyUpdated)
	if eventErr != nil || idErr != nil || partialEvent.ID != wantPartialEventID || partialEvent.Kind != workboard.CardReviseAction ||
		partialEvent.ActorID != "operator-unlock" || partialEvent.ActorType != "operator" {
		t.Fatalf("partial event=%+v eventErr=%v idErr=%v", partialEvent, eventErr, idErr)
	}
	var response []byte
	if err = store.db.QueryRow(`SELECT response FROM workboard_operations WHERE operation_id=?`, accepted.OperationID).Scan(&response); err != nil {
		t.Fatal(err)
	}
	var envelope evaluationMutationResponse
	if strictJSON(response, &envelope) != nil || len(envelope.Result.Successors) != 3 {
		t.Fatalf("response=%s", response)
	}
	unlocked, partialRecorded := 0, false
	for index, successor := range envelope.Result.Successors {
		if index > 0 && envelope.Result.Successors[index-1].ID >= successor.ID || !containsString(successor.Dependencies, card.ID) {
			t.Fatalf("successors=%+v", envelope.Result.Successors)
		}
		if successor.State == workboard.Ready && successor.RemainingDependencies == 0 {
			unlocked++
		}
		if successor.ID == partial.ID && successor.State == workboard.Backlog && successor.RemainingDependencies == 1 {
			partialRecorded = true
		}
	}
	if unlocked != 2 || !partialRecorded {
		t.Fatalf("successors=%+v", envelope.Result.Successors)
	}
	replayed, err := operator.AcceptCandidate(ctx, decision)
	if err != nil || !reflect.DeepEqual(replayed, accepted) {
		t.Fatalf("replay=%+v err=%v", replayed, err)
	}
	newTitle := "Partially unblocked after acceptance"
	revised, err := cardService.ReviseCard(ctx, workboard.ReviseCardRequest{BoardID: boardID, CardID: partial.ID,
		IdempotencyKey: "successor-later-revise", ExpectedCardRevision: partiallyUpdated.Revision, Patch: workboard.CardPatch{Title: &newTitle}})
	if err != nil || revised.Revision != partiallyUpdated.Revision+1 {
		t.Fatalf("later successor revision=%+v err=%v", revised, err)
	}
	replayed, err = operator.AcceptCandidate(ctx, decision)
	if err != nil || !reflect.DeepEqual(replayed, accepted) {
		t.Fatalf("replay after legitimate successor change=%+v err=%v", replayed, err)
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
	operator = newTestEvaluationService(t, store, workboard.Actor{ID: "operator-unlock", Type: "operator"}, evaluator, &clock)
	replayed, err = operator.AcceptCandidate(ctx, decision)
	if err != nil || !reflect.DeepEqual(replayed, accepted) {
		t.Fatalf("restart replay=%+v err=%v", replayed, err)
	}

	// Even after a successor has legitimately advanced, a canonically valid
	// operation response cannot rewrite the historical dependency effect.
	tamperedEnvelope := envelope
	tamperedEnvelope.Result.Successors = append([]workboard.Card{}, envelope.Result.Successors...)
	for index := range tamperedEnvelope.Result.Successors {
		if tamperedEnvelope.Result.Successors[index].ID == partial.ID {
			tamperedEnvelope.Result.Successors[index].RemainingDependencies = 2
		}
	}
	tamperedResponse, _ := json.Marshal(tamperedEnvelope)
	if _, err = store.db.ExecContext(ctx, `UPDATE workboard_operations SET response=? WHERE operation_id=?`, tamperedResponse, accepted.OperationID); err != nil {
		t.Fatal(err)
	}
	if _, err = operator.AcceptCandidate(ctx, decision); !errors.Is(err, ErrWorkboardCorrupt) {
		t.Fatalf("valid successor response tamper error=%v", err)
	}
	if _, err = store.db.ExecContext(ctx, `UPDATE workboard_operations SET response=? WHERE operation_id=?`, response, accepted.OperationID); err != nil {
		t.Fatal(err)
	}

	// A canonically encoded but historically false successor projection must not
	// be accepted merely because the operation response and event agree.
	readTx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	originalCard, originalBody, err := readStoredCard(ctx, readTx, boardID, first.ID)
	rollbackErr := readTx.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	if rollbackErr != nil {
		t.Fatal(rollbackErr)
	}
	tamperedCard := originalCard
	tamperedCard.Rank = formatWorkboardRank(1)
	tamperedBody := updateStoredBody(originalBody, tamperedCard)
	tamperedBytes, _ := json.Marshal(tamperedBody)
	if _, err = store.db.ExecContext(ctx, `UPDATE workboard_cards SET rank=?,body=? WHERE board_id=? AND id=?`, tamperedCard.Rank, tamperedBytes, boardID, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.GetCard(ctx, boardID, first.ID); err != nil {
		t.Fatalf("tampered projection should remain canonically valid: %v", err)
	}
	if _, err = operator.AcceptCandidate(ctx, decision); !errors.Is(err, ErrWorkboardCorrupt) {
		t.Fatalf("same-revision successor tamper error=%v", err)
	}
	originalBytes, _ := json.Marshal(originalBody)
	if _, err = store.db.ExecContext(ctx, `UPDATE workboard_cards SET rank=?,body=? WHERE board_id=? AND id=?`, originalCard.Rank, originalBytes, boardID, first.ID); err != nil {
		t.Fatal(err)
	}

	// A valid canonical event ID cannot be substituted for the immutable effect
	// digest, and removing an effect event must also invalidate exact replay.
	tamperedEvent := partialEvent
	tamperedEvent.ID = "tampered-successor-effect"
	tamperedEventBytes, _ := json.Marshal(tamperedEvent)
	if _, err = store.db.ExecContext(ctx, `UPDATE workboard_events SET id=?,body=? WHERE board_id=? AND id=?`, tamperedEvent.ID, tamperedEventBytes, boardID, partialEvent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = operator.AcceptCandidate(ctx, decision); !errors.Is(err, ErrWorkboardCorrupt) {
		t.Fatalf("valid successor event tamper error=%v", err)
	}
	partialEventBytes, _ := json.Marshal(partialEvent)
	if _, err = store.db.ExecContext(ctx, `UPDATE workboard_events SET id=?,body=? WHERE board_id=? AND id=?`, partialEvent.ID, partialEventBytes, boardID, tamperedEvent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.ExecContext(ctx, `DELETE FROM workboard_events WHERE board_id=? AND id=?`, boardID, partialEvent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = operator.AcceptCandidate(ctx, decision); !errors.Is(err, ErrWorkboardCorrupt) {
		t.Fatalf("missing successor effect error=%v", err)
	}
}

func TestWorkboardRejectionDoesNotUnlockSuccessor(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	card, boardID := readyLifecycleCard(t, ctx, store, clock)
	cardService, _ := workboard.NewCardService(store, telemetryCardAuthority{authority: workboard.Authority{CreationScope: "session", Actor: workboard.Actor{ID: "operator", Type: "operator"}}})
	successor := createCardForTest(t, ctx, cardService, boardID, "reject-successor-key", "Rejected successor", 3, 2, []string{card.ID})
	worker := workboard.Actor{ID: "worker-reject-unlock", Type: "worker"}
	clock = card.UpdatedAt.Add(time.Second)
	lifecycle := newTestLifecycleService(t, store, worker, verifiedLifecycleRecovery("reject-unlock-proof", workboard.EffectFree), &clock)
	if _, err = lifecycle.Claim(ctx, workboard.ClaimRequest{BoardID: boardID, CardID: card.ID, IdempotencyKey: "reject-unlock-claim", ExpectedCardRevision: card.Revision}); err != nil {
		t.Fatal(err)
	}
	attemptID, claimID := currentLifecycleIDs(t, store, boardID, card.ID)
	clock = clock.Add(time.Second)
	evaluator := &evaluationFixture{evidence: []workboard.EvidenceInput{{CriterionID: "tests", Source: "deterministic", Outcome: "failed", ActorID: "go-test", ActorType: "validator", Reference: "failed"}}}
	workerService := newTestEvaluationService(t, store, worker, evaluator, &clock)
	if _, err = workerService.SubmitCandidate(ctx, workboard.SubmitCandidateRequest{BoardID: boardID, CardID: card.ID, AttemptID: attemptID, ClaimID: claimID,
		IdempotencyKey: "reject-unlock-submit", ExpectedCardRevision: card.Revision + 1, ExpectedClaimRevision: 1, CriteriaRevision: card.CriteriaRevision, Summary: "rejected"}); err != nil {
		t.Fatal(err)
	}
	candidate, evidence := evaluationRows(t, store, boardID, card.ID, attemptID)
	current, _ := store.GetCard(ctx, boardID, card.ID)
	clock = clock.Add(time.Second)
	operator := newTestEvaluationService(t, store, workboard.Actor{ID: "operator-reject-unlock", Type: "operator"}, evaluator, &clock)
	receipt, err := operator.RejectCandidate(ctx, workboard.DecideCandidateRequest{BoardID: boardID, CardID: card.ID, AttemptID: attemptID, CandidateID: candidate.ID,
		IdempotencyKey: "reject-unlock-decision", ExpectedCardRevision: current.Revision, CriteriaRevision: card.CriteriaRevision,
		CandidateDigest: candidate.Digest, CriteriaDigest: candidate.CriteriaDigest, EvidenceHeadRevision: int64(len(evidence)),
		EvidenceSetDigest: workboard.EvidenceSetDigest(evidence), PolicyDigest: candidate.PolicyDigest, Evidence: "tests failed"})
	updated, readErr := store.GetCard(ctx, boardID, successor.ID)
	if err != nil || readErr != nil || receipt.EventCount != 1 || updated.State != workboard.Backlog || updated.RemainingDependencies != 1 || updated.Revision != successor.Revision {
		t.Fatalf("receipt=%+v successor=%+v err=%v read=%v", receipt, updated, err, readErr)
	}
}

func TestWorkboardAcceptanceSuccessorRollback(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	card, boardID := readyLifecycleCard(t, ctx, store, clock)
	cardService, _ := workboard.NewCardService(store, telemetryCardAuthority{authority: workboard.Authority{CreationScope: "session", Actor: workboard.Actor{ID: "operator", Type: "operator"}}})
	first := createCardForTest(t, ctx, cardService, boardID, "rollback-successor-one", "Rollback one", 3, 2, []string{card.ID})
	second := createCardForTest(t, ctx, cardService, boardID, "rollback-successor-two", "Rollback two", 4, 3, []string{card.ID})
	worker := workboard.Actor{ID: "worker-unlock-rollback", Type: "worker"}
	clock = card.UpdatedAt.Add(time.Second)
	lifecycle := newTestLifecycleService(t, store, worker, verifiedLifecycleRecovery("unlock-rollback-proof", workboard.EffectFree), &clock)
	if _, err = lifecycle.Claim(ctx, workboard.ClaimRequest{BoardID: boardID, CardID: card.ID, IdempotencyKey: "unlock-rollback-claim", ExpectedCardRevision: card.Revision}); err != nil {
		t.Fatal(err)
	}
	attemptID, claimID := currentLifecycleIDs(t, store, boardID, card.ID)
	clock = clock.Add(time.Second)
	evaluator := &evaluationFixture{evidence: []workboard.EvidenceInput{{CriterionID: "tests", Source: "deterministic", Outcome: "passed", ActorID: "go-test", ActorType: "validator", Reference: "passed"}}}
	workerService := newTestEvaluationService(t, store, worker, evaluator, &clock)
	if _, err = workerService.SubmitCandidate(ctx, workboard.SubmitCandidateRequest{BoardID: boardID, CardID: card.ID, AttemptID: attemptID, ClaimID: claimID,
		IdempotencyKey: "unlock-rollback-submit", ExpectedCardRevision: card.Revision + 1, ExpectedClaimRevision: 1, CriteriaRevision: card.CriteriaRevision, Summary: "rollback"}); err != nil {
		t.Fatal(err)
	}
	candidate, evidence := evaluationRows(t, store, boardID, card.ID, attemptID)
	current, _ := store.GetCard(ctx, boardID, card.ID)
	var priorSequence int64
	if err = store.db.QueryRow(`SELECT event_sequence FROM workboard_boards WHERE id=?`, boardID).Scan(&priorSequence); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`CREATE TRIGGER fail_successor_unlock_event BEFORE INSERT ON workboard_events WHEN NEW.kind='card.move' BEGIN SELECT RAISE(ABORT,'injected successor unlock failure'); END`); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Second)
	operator := newTestEvaluationService(t, store, workboard.Actor{ID: "operator-unlock-rollback", Type: "operator"}, evaluator, &clock)
	decision := workboard.DecideCandidateRequest{BoardID: boardID, CardID: card.ID, AttemptID: attemptID, CandidateID: candidate.ID,
		IdempotencyKey: "unlock-rollback-accept", ExpectedCardRevision: current.Revision, CriteriaRevision: card.CriteriaRevision,
		CandidateDigest: candidate.Digest, CriteriaDigest: candidate.CriteriaDigest, EvidenceHeadRevision: int64(len(evidence)),
		EvidenceSetDigest: workboard.EvidenceSetDigest(evidence), PolicyDigest: candidate.PolicyDigest, Evidence: "rollback first"}
	if _, err = operator.AcceptCandidate(ctx, decision); err == nil || !strings.Contains(err.Error(), "injected successor unlock failure") {
		t.Fatalf("rollback error=%v", err)
	}
	primary, _ := store.GetCard(ctx, boardID, card.ID)
	for _, successor := range []workboard.Card{first, second} {
		updated, readErr := store.GetCard(ctx, boardID, successor.ID)
		if readErr != nil || updated.State != workboard.Backlog || updated.RemainingDependencies != 1 || updated.Revision != successor.Revision {
			t.Fatalf("rolled back successor=%+v err=%v", updated, readErr)
		}
	}
	var sequence, acceptances int64
	if err = store.db.QueryRow(`SELECT event_sequence FROM workboard_boards WHERE id=?`, boardID).Scan(&sequence); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow(`SELECT count(*) FROM workboard_acceptances WHERE attempt_id=?`, attemptID).Scan(&acceptances); err != nil {
		t.Fatal(err)
	}
	if primary.State != workboard.Review || sequence != priorSequence || acceptances != 0 {
		t.Fatalf("primary=%+v sequence=%d/%d acceptances=%d", primary, sequence, priorSequence, acceptances)
	}
	if _, err = store.db.Exec(`DROP TRIGGER fail_successor_unlock_event`); err != nil {
		t.Fatal(err)
	}
	if receipt, retryErr := operator.AcceptCandidate(ctx, decision); retryErr != nil || receipt.EventCount != 3 {
		t.Fatalf("retry=%+v err=%v", receipt, retryErr)
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
