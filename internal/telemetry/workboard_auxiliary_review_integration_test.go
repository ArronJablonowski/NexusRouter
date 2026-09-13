package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

type budgetedReviewFixture struct {
	calls       atomic.Int64
	entered     chan struct{}
	release     chan struct{}
	panic       bool
	timeLimitMS int64
	measured    *workboard.AuxiliaryReviewMeasurements
	evidence    []workboard.EvidenceInput
	captured    chan workboard.CandidateEvaluationRequest
	onEvaluate  func()
	auditMutate func(*evaluation.AuditRecord)
	now         func() time.Time
}

func (e *budgetedReviewFixture) EvaluateCandidate(context.Context, workboard.CandidateEvaluationRequest) ([]workboard.EvidenceInput, error) {
	return nil, errors.New("unbudgeted evaluator path used")
}

func (e *budgetedReviewFixture) AuxiliaryReviewReservation(frozen workboard.CandidateEvaluationRequest) (workboard.AuxiliaryReviewReservation, error) {
	timeLimitMS := e.timeLimitMS
	if timeLimitMS == 0 {
		timeLimitMS = 5_000
	}
	return workboard.AuxiliaryReviewReservation{Version: 1, BoardID: frozen.BoardID, CardID: frozen.CardID,
		AttemptID: frozen.AttemptID, ClaimID: frozen.ClaimID, CandidateID: frozen.CandidateID,
		CandidateDigest: frozen.CandidateDigest, CriteriaDigest: frozen.CriteriaDigest, PolicyDigest: frozen.PolicyDigest,
		ReviewerID: "independent-reviewer", ModelID: "review-model", ProviderID: "review-provider",
		ConfigID: strings.Repeat("e", 64), TimeLimitMS: timeLimitMS, TokenLimit: 1_000, CostMicros: 400}, nil
}

func (e *budgetedReviewFixture) EvaluateBudgetedCandidate(ctx context.Context, frozen workboard.CandidateEvaluationRequest) (workboard.BudgetedCandidateEvaluation, error) {
	e.calls.Add(1)
	if e.onEvaluate != nil {
		e.onEvaluate()
	}
	if e.captured != nil {
		e.captured <- frozen
	}
	if e.panic {
		panic("review fixture panic")
	}
	if e.entered != nil {
		select {
		case e.entered <- struct{}{}:
		case <-ctx.Done():
			return workboard.BudgetedCandidateEvaluation{}, ctx.Err()
		}
	}
	if e.release != nil {
		select {
		case <-e.release:
		case <-ctx.Done():
			return workboard.BudgetedCandidateEvaluation{}, ctx.Err()
		}
	}
	measurements := workboard.AuxiliaryReviewMeasurements{}
	if e.measured != nil {
		measurements = *e.measured
	} else {
		timeMS, tokens, cost := int64(25), int64(12), int64(7)
		measurements = workboard.AuxiliaryReviewMeasurements{TimeMS: &timeMS, Tokens: &tokens, CostMicros: &cost}
	}
	auditTime := time.Now().UTC()
	if e.now != nil {
		auditTime = e.now().UTC()
	}
	audit := budgetedReviewAudit(frozen, auditTime)
	if e.auditMutate != nil {
		e.auditMutate(&audit)
	}
	evidence := append([]workboard.EvidenceInput(nil), e.evidence...)
	for i := range evidence {
		if evidence[i].Source == "model_audit" && evidence[i].ActorID == "independent-reviewer" {
			evidence[i].Reference = audit.ID
		}
	}
	return workboard.BudgetedCandidateEvaluation{Evidence: evidence, Measurements: measurements, Audit: audit}, nil
}

func budgetedReviewAudit(frozen workboard.CandidateEvaluationRequest, auditTime time.Time) evaluation.AuditRecord {
	refs := []string{"requirements", "candidate", "candidate_claim", "source_binding"}
	for index := range frozen.Criteria {
		refs = append(refs, "criterion_"+fmt.Sprintf("%02d", index))
	}
	return evaluation.AuditRecord{Version: 1, ID: "review-audit-" + frozen.CandidateID, TaskID: frozen.SourceTaskID,
		AttemptID: frozen.SourceAttemptID, EvaluatorModel: "review-model", EvaluatorProvider: "review-provider",
		Audit: evaluation.Audit{Version: 1, EvaluatorID: "independent-reviewer", RubricVersion: "fixture-v1",
			Domain: frozen.SourceDomain, Verdict: "abstain", Findings: []evaluation.AuditFinding{}},
		EvidenceRefs: refs, Usage: &providers.Usage{InputTokens: 4, OutputTokens: 8},
		Elapsed: 25 * time.Millisecond, Time: auditTime}
}

func TestEvaluationServiceBudgetedEvaluatorPreservesLegacyCandidateReplay(t *testing.T) {
	ctx := context.Background()
	store := openExecutionAdmissionStore(t, ctx)
	defer store.Close()
	clock := time.Date(2026, 9, 12, 18, 30, 0, 0, time.UTC)
	legacy := &evaluationFixture{}
	service, request := prepareBudgetedReviewService(t, store, legacy, &clock, "legacy-replay")
	original, err := service.SubmitCandidate(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	budgeted := &budgetedReviewFixture{}
	replay := newTestEvaluationService(t, store, workboard.Actor{ID: "integrated-worker-legacy-replay", Type: "worker"}, budgeted, &clock)
	receipt, err := replay.SubmitCandidate(ctx, request)
	if err != nil || receipt.OperationID != original.OperationID || budgeted.calls.Load() != 0 {
		t.Fatalf("receipt=%+v calls=%d err=%v", receipt, budgeted.calls.Load(), err)
	}
}

func TestEvaluationServiceRollsBackCandidateWhenCompletedSettlementFails(t *testing.T) {
	ctx := context.Background()
	store := openExecutionAdmissionStore(t, ctx)
	defer store.Close()
	clock := time.Date(2026, 9, 12, 18, 45, 0, 0, time.UTC)
	evaluator := &budgetedReviewFixture{captured: make(chan workboard.CandidateEvaluationRequest, 1)}
	service, request := prepareBudgetedReviewService(t, store, evaluator, &clock, "settlement-rollback")
	if _, err := store.db.Exec(`CREATE TRIGGER test_reject_completed_review BEFORE INSERT ON workboard_auxiliary_review_settlements
		WHEN NEW.disposition='completed' BEGIN SELECT RAISE(ABORT,'test completed settlement failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SubmitCandidate(ctx, request); err == nil {
		t.Fatal("completed settlement failure accepted")
	}
	var candidates int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM workboard_candidates WHERE board_id=? AND card_id=? AND attempt_id=?`,
		request.BoardID, request.CardID, request.AttemptID).Scan(&candidates); err != nil || candidates != 0 {
		t.Fatalf("candidates=%d err=%v", candidates, err)
	}
	var disposition string
	if err := store.db.QueryRowContext(ctx, `SELECT disposition FROM workboard_auxiliary_review_settlements`).Scan(&disposition); err != nil || disposition != "failed" {
		t.Fatalf("disposition=%q err=%v", disposition, err)
	}
}

func TestEvaluationServiceRejectsReviewOverrunAndContainsPanic(t *testing.T) {
	for _, test := range []struct {
		name      string
		evaluator *budgetedReviewFixture
	}{
		{name: "overrun", evaluator: func() *budgetedReviewFixture {
			tokens := int64(1_001)
			m := workboard.AuxiliaryReviewMeasurements{Tokens: &tokens}
			return &budgetedReviewFixture{measured: &m}
		}()},
		{name: "panic", evaluator: &budgetedReviewFixture{panic: true}},
		{name: "forbidden-user-feedback", evaluator: &budgetedReviewFixture{evidence: []workboard.EvidenceInput{{
			CriterionID: "tests", Source: "user_feedback", Outcome: "passed", ActorID: "operator", ActorType: "operator", Reference: "spoofed-feedback",
		}}}},
		{name: "spoofed-reviewer", evaluator: &budgetedReviewFixture{evidence: []workboard.EvidenceInput{{
			CriterionID: "tests", Source: "model_audit", Outcome: "passed", ActorID: "other-reviewer", ActorType: "model", Reference: "spoofed-review",
		}}}},
		{name: "spoofed-audit-model", evaluator: &budgetedReviewFixture{auditMutate: func(a *evaluation.AuditRecord) {
			a.EvaluatorModel = "other-model"
		}}},
		{name: "spoofed-audit-domain", evaluator: &budgetedReviewFixture{auditMutate: func(a *evaluation.AuditRecord) {
			a.Audit.Domain = "other-domain"
		}}},
		{name: "audit-before-admission", evaluator: &budgetedReviewFixture{auditMutate: func(a *evaluation.AuditRecord) {
			a.Time = time.Unix(1, 0).UTC()
		}}},
		{name: "audit-start-before-admission", evaluator: &budgetedReviewFixture{auditMutate: func(a *evaluation.AuditRecord) {
			a.Time = a.Time.Add(-10 * time.Millisecond)
		}}},
		{name: "audit-after-commit", evaluator: &budgetedReviewFixture{auditMutate: func(a *evaluation.AuditRecord) {
			a.Time = time.Date(2260, 1, 1, 0, 0, 0, 0, time.UTC)
		}}},
		{name: "abstain-with-evidence", evaluator: &budgetedReviewFixture{evidence: []workboard.EvidenceInput{{
			CriterionID: "tests", Source: "model_audit", Outcome: "passed", ActorID: "independent-reviewer", ActorType: "model",
		}}}},
		{name: "reject-with-passed-evidence", evaluator: &budgetedReviewFixture{evidence: []workboard.EvidenceInput{{
			CriterionID: "tests", Source: "model_audit", Outcome: "passed", ActorID: "independent-reviewer", ActorType: "model",
		}}, auditMutate: func(a *evaluation.AuditRecord) {
			a.Audit.Verdict = "reject"
			a.Audit.Findings = []evaluation.AuditFinding{{Summary: "test failure", EvidenceRefs: []string{"criterion_00"}}}
		}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store := openExecutionAdmissionStore(t, ctx)
			defer store.Close()
			clock := time.Date(2026, 9, 12, 20, 0, 0, 0, time.UTC)
			service, request := prepareBudgetedReviewService(t, store, test.evaluator, &clock, "failure-"+test.name)
			if _, err := service.SubmitCandidate(ctx, request); err == nil {
				t.Fatal("invalid review accepted")
			}
			var candidates int
			if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM workboard_candidates WHERE board_id=? AND card_id=? AND attempt_id=?`,
				request.BoardID, request.CardID, request.AttemptID).Scan(&candidates); err != nil || candidates != 0 {
				t.Fatalf("candidates=%d err=%v", candidates, err)
			}
			var disposition string
			if err := store.db.QueryRowContext(ctx, `SELECT disposition FROM workboard_auxiliary_review_settlements`).Scan(&disposition); err != nil || disposition != "failed" {
				t.Fatalf("disposition=%q err=%v", disposition, err)
			}
		})
	}
}

func TestEvaluationServiceEnforcesAuxiliaryReviewDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	store := openExecutionAdmissionStore(t, ctx)
	defer store.Close()
	clock := time.Date(2026, 9, 12, 20, 30, 0, 0, time.UTC)
	evaluator := &budgetedReviewFixture{timeLimitMS: 100, release: make(chan struct{})}
	service, request := prepareBudgetedReviewService(t, store, evaluator, &clock, "deadline")

	started := time.Now()
	if _, err := service.SubmitCandidate(ctx, request); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline was not enforced: %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("deadline enforcement took %s", elapsed)
	}
	var candidates int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM workboard_candidates WHERE board_id=? AND card_id=? AND attempt_id=?`,
		request.BoardID, request.CardID, request.AttemptID).Scan(&candidates); err != nil || candidates != 0 {
		t.Fatalf("candidates=%d err=%v", candidates, err)
	}
	var disposition string
	if err := store.db.QueryRowContext(ctx, `SELECT disposition FROM workboard_auxiliary_review_settlements`).Scan(&disposition); err != nil || disposition != "failed" {
		t.Fatalf("disposition=%q err=%v", disposition, err)
	}
}

func TestAuxiliaryReviewSuccessorFencePermitsExpiredClaimBeforeReviewDeadline(t *testing.T) {
	ctx := context.Background()
	store := openExecutionAdmissionStore(t, ctx)
	defer store.Close()
	clock := time.Date(2026, 9, 12, 20, 40, 0, 0, time.UTC)
	evaluator := &budgetedReviewFixture{}
	service, request := prepareBudgetedReviewService(t, store, evaluator, &clock, "expired-claim-successor")
	var expiresAtNS int64
	if err := store.db.QueryRowContext(ctx, `SELECT expires_at FROM workboard_claims WHERE id=?`, request.ClaimID).Scan(&expiresAtNS); err != nil {
		t.Fatal(err)
	}
	expiresAt := time.Unix(0, expiresAtNS).UTC()
	clock = expiresAt.Add(-50 * time.Millisecond)
	evaluator.onEvaluate = func() { clock = expiresAt.Add(50 * time.Millisecond) }

	receipt, err := service.SubmitCandidate(ctx, request)
	if err != nil || receipt.Validate() != nil || evaluator.calls.Load() != 1 {
		t.Fatalf("receipt=%+v calls=%d err=%v", receipt, evaluator.calls.Load(), err)
	}
}

func TestAuxiliaryReviewSuccessorFenceRejectsCompletionAfterReviewDeadline(t *testing.T) {
	ctx := context.Background()
	store := openExecutionAdmissionStore(t, ctx)
	defer store.Close()
	clock := time.Date(2026, 9, 12, 20, 50, 0, 0, time.UTC)
	evaluator := &budgetedReviewFixture{}
	service, request := prepareBudgetedReviewService(t, store, evaluator, &clock, "expired-review-deadline")
	admittedAt := clock
	evaluator.onEvaluate = func() { clock = admittedAt.Add(5 * time.Second) }

	if _, err := service.SubmitCandidate(ctx, request); !errors.Is(err, &workboard.Violation{Code: workboard.CodeLeaseExpired}) {
		t.Fatalf("late review completion accepted: %v", err)
	}
	var candidates int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM workboard_candidates WHERE board_id=? AND card_id=? AND attempt_id=?`,
		request.BoardID, request.CardID, request.AttemptID).Scan(&candidates); err != nil || candidates != 0 {
		t.Fatalf("candidates=%d err=%v", candidates, err)
	}
	var disposition string
	if err := store.db.QueryRowContext(ctx, `SELECT disposition FROM workboard_auxiliary_review_settlements`).Scan(&disposition); err != nil || disposition != "failed" {
		t.Fatalf("disposition=%q err=%v", disposition, err)
	}
}

func TestAuxiliaryReviewSuccessorFenceRejectsHeartbeatRevisionAdvance(t *testing.T) {
	ctx := context.Background()
	store := openExecutionAdmissionStore(t, ctx)
	defer store.Close()
	clock := time.Date(2026, 9, 12, 20, 55, 0, 0, time.UTC)
	evaluator := &budgetedReviewFixture{}
	service, request := prepareBudgetedReviewService(t, store, evaluator, &clock, "heartbeat-revision")
	evaluator.onEvaluate = func() {
		clock = clock.Add(10 * time.Millisecond)
		lifecycle := newTestLifecycleService(t, store, workboard.Actor{ID: "integrated-worker-heartbeat-revision", Type: "worker"},
			verifiedLifecycleRecovery("review-heartbeat-proof", workboard.EffectFree), &clock)
		if _, err := lifecycle.Heartbeat(ctx, workboard.HeartbeatRequest{BoardID: request.BoardID, CardID: request.CardID,
			AttemptID: request.AttemptID, ClaimID: request.ClaimID, IdempotencyKey: "review-heartbeat-revision-advance",
			ExpectedClaimRevision: request.ExpectedClaimRevision}); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := service.SubmitCandidate(ctx, request); !errors.Is(err, &workboard.Violation{Code: workboard.CodeStaleRevision}) {
		t.Fatalf("review completion survived heartbeat revision: %v", err)
	}
	var candidates int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM workboard_candidates WHERE board_id=? AND card_id=? AND attempt_id=?`,
		request.BoardID, request.CardID, request.AttemptID).Scan(&candidates); err != nil || candidates != 0 {
		t.Fatalf("candidates=%d err=%v", candidates, err)
	}
}

func prepareBudgetedReviewService(t *testing.T, store *Store, evaluator workboard.CandidateEvaluator,
	clock *time.Time, suffix string,
) (*workboard.EvaluationService, workboard.SubmitCandidateRequest) {
	t.Helper()
	ctx := context.Background()
	card, boardID := createReadyBudgetCard(t, ctx, store, *clock, "integrated-review-"+suffix, workboardTestBudget())
	*clock = card.UpdatedAt.Add(time.Second)
	event := budgetedStart("integrated-worker-"+suffix, "integrated-task-"+suffix, "integrated-session-"+suffix, *clock, .0005)
	execution := executionReservation(event, 10_000, 2_000, 500, 4, 2)
	receipt, err := claimBudgetedStart(t, ctx, store, clock, card, boardID, event, execution, "integrated-claim-"+suffix)
	if err != nil || receipt.CardRevision == nil || receipt.ClaimRevision == nil {
		t.Fatal("claim", receipt, err)
	}
	attemptID, claimID := currentLifecycleIDs(t, store, boardID, card.ID)
	terminal := appendBudgetRuntime(t, ctx, store, event, &providers.Usage{InputTokens: 4, OutputTokens: 3}, event.Time.Add(2*time.Second), runtime.TaskCompleted)
	*clock = terminal.Time.Add(time.Second)
	if fixture, ok := evaluator.(*budgetedReviewFixture); ok && fixture.now == nil {
		fixture.now = func() time.Time {
			*clock = clock.Add(25 * time.Millisecond)
			return *clock
		}
	}
	service := newTestEvaluationService(t, store, workboard.Actor{ID: event.WorkerID, Type: "worker"}, evaluator, clock)
	request := workboard.SubmitCandidateRequest{BoardID: boardID, CardID: card.ID, AttemptID: attemptID, ClaimID: claimID,
		IdempotencyKey: "integrated-candidate-" + suffix, ExpectedCardRevision: *receipt.CardRevision,
		ExpectedClaimRevision: *receipt.ClaimRevision, CriteriaRevision: card.CriteriaRevision,
		Summary: "candidate for integrated bounded auxiliary review", ArtifactRefs: []string{}}
	return service, request
}

func TestEvaluationServiceAtomicallyCommitsBudgetedReviewAndCandidate(t *testing.T) {
	ctx := context.Background()
	store := openExecutionAdmissionStore(t, ctx)
	defer store.Close()
	clock := time.Date(2026, 9, 12, 18, 0, 0, 0, time.UTC)
	evaluator := &budgetedReviewFixture{captured: make(chan workboard.CandidateEvaluationRequest, 1)}
	service, request := prepareBudgetedReviewService(t, store, evaluator, &clock, "atomic")
	receipt, err := service.SubmitCandidate(ctx, request)
	if err != nil || receipt.Validate() != nil || evaluator.calls.Load() != 1 {
		t.Fatalf("receipt=%+v calls=%d err=%v", receipt, evaluator.calls.Load(), err)
	}
	frozen := <-evaluator.captured

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	operation := "candidate-review-" + candidateIDForAttempt(t, tx, request.BoardID, request.CardID, request.AttemptID)
	admission, found, err := readAuxiliaryReviewAdmissionByOperation(ctx, tx, operation)
	if err != nil || !found {
		t.Fatal("admission", found, err)
	}
	settlement, found, err := readAuxiliaryReviewSettlement(ctx, tx, admission.AdmissionID)
	if err != nil || !found || settlement.Disposition != workboard.AuxiliaryReviewCompleted || settlement.ChargedTokens != 12 || settlement.ChargedCostMicros != 7 {
		t.Fatalf("settlement=%+v found=%v err=%v", settlement, found, err)
	}
	outcome, found, err := readAuxiliaryReviewOutcome(ctx, tx, admission.AdmissionID)
	if err != nil || !found || outcome.ValidateBindings(frozen, admission, settlement) != nil || outcome.AuditID != "review-audit-"+frozen.CandidateID ||
		outcome.EvidenceCount != 0 || outcome.EvidenceDigest != workboard.EvidenceSetDigest([]workboard.EvidenceRecord{}) {
		t.Fatalf("outcome=%+v found=%v err=%v", outcome, found, err)
	}
	var auditBody []byte
	if err = tx.QueryRowContext(ctx, `SELECT body FROM audit_records WHERE id=? AND task_id=?`, outcome.AuditID, frozen.SourceTaskID).Scan(&auditBody); err != nil {
		t.Fatal("structured audit missing from atomic commit", err)
	}
	var storedAudit evaluation.AuditRecord
	if json.Unmarshal(auditBody, &storedAudit) != nil || storedAudit.Validate() != nil {
		t.Fatal("invalid structured audit in atomic commit")
	}
	auditDigest, err := evaluation.AuditRecordDigest(storedAudit)
	if err != nil || auditDigest != outcome.AuditDigest {
		t.Fatalf("audit digest=%q outcome=%q err=%v", auditDigest, outcome.AuditDigest, err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	publicOutcome, found, legacy, err := store.ReplayAuxiliaryReviewOutcome(ctx, operation)
	if err != nil || !found || legacy || publicOutcome.OutcomeDigest != outcome.OutcomeDigest {
		t.Fatalf("outcome replay=%+v found=%v legacy=%v err=%v", publicOutcome, found, legacy, err)
	}
	mutation := workboard.EvaluationMutation{Version: 1, Kind: workboard.EvaluationCandidateSubmit, BoardID: request.BoardID,
		CardID: request.CardID, AttemptID: request.AttemptID, ClaimID: request.ClaimID, CandidateID: frozen.CandidateID,
		IdempotencyKey: request.IdempotencyKey, Actor: workboard.Actor{ID: frozen.WorkerID, Type: "worker"},
		ExpectedCardRevision: request.ExpectedCardRevision, ExpectedClaimRevision: request.ExpectedClaimRevision,
		CriteriaRevision: request.CriteriaRevision, CandidateDigest: frozen.CandidateDigest,
		Summary: request.Summary, ArtifactRefs: request.ArtifactRefs, Evaluated: []workboard.EvidenceInput{},
		Now: time.Now().UTC()}
	mutation.RequestDigest, err = workboard.EvaluationDigest(mutation)
	if err != nil {
		t.Fatal(err)
	}
	timeMS, tokens, cost := int64(25), int64(12), int64(7)
	measurements := workboard.AuxiliaryReviewMeasurements{TimeMS: &timeMS, Tokens: &tokens, CostMicros: &cost}
	replayedAtomic, replayedSettlement, replayedOutcome, err := store.ApplyEvaluationMutationAndSettleAuxiliaryReview(ctx, frozen, mutation, storedAudit, time.Now, operation, measurements)
	if err != nil || replayedAtomic.OperationID != receipt.OperationID || replayedAtomic.ResponseDigest != receipt.ResponseDigest || replayedSettlement != settlement || replayedOutcome.AuditID == "" {
		t.Fatalf("atomic replay receipt=%+v settlement=%+v err=%v", replayedAtomic, replayedSettlement, err)
	}
	driftedTokens := int64(13)
	measurements.Tokens = &driftedTokens
	if _, _, _, err = store.ApplyEvaluationMutationAndSettleAuxiliaryReview(ctx, frozen, mutation, storedAudit, time.Now, operation, measurements); err == nil {
		t.Fatalf("atomic replay measurement drift accepted: %v", err)
	}
	measurements.Tokens = &tokens
	replayed, err := service.SubmitCandidate(ctx, request)
	if err != nil || replayed.ResponseDigest != receipt.ResponseDigest || replayed.OperationID != receipt.OperationID || evaluator.calls.Load() != 1 {
		t.Fatalf("replay=%+v calls=%d err=%v", replayed, evaluator.calls.Load(), err)
	}
	if _, err = store.db.Exec(`DROP TRIGGER workboard_auxiliary_review_successor_fence_immutable_update;
		UPDATE workboard_auxiliary_review_successor_fences SET body=json_set(body,'$.claim_revision',claim_revision+1) WHERE admission_id=?`, admission.AdmissionID); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = store.ApplyEvaluationMutationAndSettleAuxiliaryReview(ctx, frozen, mutation, storedAudit, time.Now, operation, measurements); !errors.Is(err, ErrWorkboardCorrupt) {
		t.Fatalf("completed replay ignored corrupt schema-44 fence: %v", err)
	}
	var databasePath string
	if err = store.db.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&databasePath); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`DROP TRIGGER IF EXISTS workboard_auxiliary_review_successor_fence_immutable_delete;
		DROP TRIGGER IF EXISTS workboard_auxiliary_review_successor_fence_immutable_update;
		DROP TRIGGER IF EXISTS workboard_auxiliary_review_successor_fence_binding;
		DROP TABLE workboard_auxiliary_review_successor_fences;
		DROP TRIGGER workboard_auxiliary_review_legacy_admission_sealed_insert;
		DROP TRIGGER workboard_auxiliary_review_legacy_admission_immutable_update;
		DROP TRIGGER workboard_auxiliary_review_legacy_admission_immutable_delete;
		DROP TABLE workboard_auxiliary_review_legacy_admissions;
		PRAGMA user_version=43`); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, openErr := Open(ctx, databasePath); openErr == nil {
		reopened.Close()
		t.Fatal("downgraded declaration retained schema-45 outcome authority")
	}
}

func TestAuxiliaryReviewOutcomeReplayRereadsCanonicalEvidence(t *testing.T) {
	ctx := context.Background()
	store := openExecutionAdmissionStore(t, ctx)
	defer store.Close()
	clock := time.Date(2026, 9, 12, 19, 0, 0, 0, time.UTC)
	evaluator := &budgetedReviewFixture{
		evidence: []workboard.EvidenceInput{{CriterionID: "tests", Source: "model_audit", Outcome: "failed", ActorID: "independent-reviewer", ActorType: "model"}},
		auditMutate: func(a *evaluation.AuditRecord) {
			a.Audit.Verdict = "reject"
			a.Audit.Findings = []evaluation.AuditFinding{{Summary: "test failure", EvidenceRefs: []string{"criterion_00"}}}
		},
	}
	service, request := prepareBudgetedReviewService(t, store, evaluator, &clock, "evidence-replay")
	if _, err := service.SubmitCandidate(ctx, request); err != nil {
		t.Fatal(err)
	}
	var candidateID string
	if err := store.db.QueryRowContext(ctx, `SELECT id FROM workboard_candidates WHERE board_id=? AND card_id=? AND attempt_id=?`,
		request.BoardID, request.CardID, request.AttemptID).Scan(&candidateID); err != nil {
		t.Fatal(err)
	}
	operation := "candidate-review-" + candidateID
	if _, found, legacy, err := store.ReplayAuxiliaryReviewOutcome(ctx, operation); err != nil || !found || legacy {
		t.Fatalf("valid outcome did not replay: found=%v legacy=%v err=%v", found, legacy, err)
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM workboard_evidence WHERE board_id=? AND card_id=? AND attempt_id=? AND candidate_id=?`,
		request.BoardID, request.CardID, request.AttemptID, candidateID); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := store.ReplayAuxiliaryReviewOutcome(ctx, operation); !errors.Is(err, ErrWorkboardCorrupt) {
		t.Fatalf("missing canonical evidence replayed: %v", err)
	}
}

func TestEvaluationServiceNeverRedispatchesAnExistingReviewAdmission(t *testing.T) {
	ctx := context.Background()
	store := openExecutionAdmissionStore(t, ctx)
	defer store.Close()
	clock := time.Date(2026, 9, 12, 19, 0, 0, 0, time.UTC)
	evaluator := &budgetedReviewFixture{entered: make(chan struct{}, 1), release: make(chan struct{})}
	first, request := prepareBudgetedReviewService(t, store, evaluator, &clock, "no-redispatch")
	second := newTestEvaluationService(t, store, workboard.Actor{ID: "integrated-worker-no-redispatch", Type: "worker"}, evaluator, &clock)
	firstResult := make(chan error, 1)
	go func() {
		_, err := first.SubmitCandidate(ctx, request)
		firstResult <- err
	}()
	select {
	case <-evaluator.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("review did not start")
	}
	if _, err := second.SubmitCandidate(ctx, request); !errors.Is(err, &workboard.Violation{Code: workboard.CodeIllegalTransition}) {
		t.Fatalf("existing admission did not fail closed: %v", err)
	}
	if evaluator.calls.Load() != 1 {
		t.Fatalf("review redispatched: %d calls", evaluator.calls.Load())
	}
	close(evaluator.release)
	if err := <-firstResult; err != nil {
		t.Fatal(err)
	}
}

func TestAtomicReviewCommitSamplesClockAfterWriterWait(t *testing.T) {
	ctx := context.Background()
	store := openExecutionAdmissionStore(t, ctx)
	defer store.Close()
	clock := time.Date(2026, 9, 12, 21, 0, 0, 0, time.UTC)
	card, boardID := createReadyBudgetCard(t, ctx, store, clock, "integrated-review-writer-clock", workboardTestBudget())
	clock = card.UpdatedAt.Add(time.Second)
	event := budgetedStart("integrated-worker-writer-clock", "integrated-task-writer-clock", "integrated-session-writer-clock", clock, .0005)
	execution := executionReservation(event, 10_000, 2_000, 500, 4, 2)
	receipt, err := claimBudgetedStart(t, ctx, store, &clock, card, boardID, event, execution, "integrated-claim-writer-clock")
	if err != nil || receipt.CardRevision == nil || receipt.ClaimRevision == nil {
		t.Fatal("claim", receipt, err)
	}
	attemptID, claimID := currentLifecycleIDs(t, store, boardID, card.ID)
	terminal := appendBudgetRuntime(t, ctx, store, event, &providers.Usage{InputTokens: 4, OutputTokens: 3}, event.Time.Add(2*time.Second), runtime.TaskCompleted)
	baseTime := terminal.Time.Add(time.Second)
	committedAt := baseTime.Add(time.Second)
	evaluator := &budgetedReviewFixture{entered: make(chan struct{}, 1), release: make(chan struct{}), now: func() time.Time { return baseTime.Add(25 * time.Millisecond) }}
	var clockCalls atomic.Int64
	clockCalled := make(chan struct{}, 1)
	service, err := workboard.NewEvaluationService(store, telemetryCardAuthority{authority: workboard.Authority{
		CreationScope: "acceptance-authority", Actor: workboard.Actor{ID: event.WorkerID, Type: "worker"},
	}}, evaluator, func() time.Time {
		if clockCalls.Add(1) >= 4 {
			select {
			case clockCalled <- struct{}{}:
			default:
			}
			return committedAt
		}
		return baseTime
	})
	if err != nil {
		t.Fatal(err)
	}
	request := workboard.SubmitCandidateRequest{BoardID: boardID, CardID: card.ID, AttemptID: attemptID, ClaimID: claimID,
		IdempotencyKey: "integrated-candidate-writer-clock", ExpectedCardRevision: *receipt.CardRevision,
		ExpectedClaimRevision: *receipt.ClaimRevision, CriteriaRevision: card.CriteriaRevision,
		Summary: "candidate for writer clock review", ArtifactRefs: []string{}}
	result := make(chan error, 1)
	go func() {
		_, submitErr := service.SubmitCandidate(ctx, request)
		result <- submitErr
	}()
	select {
	case <-evaluator.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("review did not start")
	}
	blocker, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = reserveWorkboardWriter(ctx, blocker); err != nil {
		blocker.Rollback()
		t.Fatal(err)
	}
	close(evaluator.release)
	select {
	case <-clockCalled:
		blocker.Rollback()
		t.Fatal("clock sampled before writer ownership")
	case <-time.After(100 * time.Millisecond):
	}
	if err = blocker.Rollback(); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-result:
	case <-time.After(5 * time.Second):
		t.Fatal("atomic commit remained blocked")
	}
	if err != nil {
		t.Fatal(err)
	}
	var settledAtNS int64
	if err = store.db.QueryRowContext(ctx, `SELECT settled_at FROM workboard_auxiliary_review_settlements`).Scan(&settledAtNS); err != nil ||
		!time.Unix(0, settledAtNS).UTC().Equal(committedAt) {
		t.Fatalf("settled_at=%d err=%v", settledAtNS, err)
	}
}

func candidateIDForAttempt(t *testing.T, tx *sql.Tx, boardID, cardID, attemptID string) string {
	t.Helper()
	var candidateID string
	if err := tx.QueryRowContext(context.Background(), `SELECT id FROM workboard_candidates WHERE board_id=? AND card_id=? AND attempt_id=?`,
		boardID, cardID, attemptID).Scan(&candidateID); err != nil {
		t.Fatal(err)
	}
	return candidateID
}
