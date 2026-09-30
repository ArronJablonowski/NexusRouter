package telemetry

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/workboard"
)

type advancingEvaluationFixture struct{ clock *time.Time }

func (e advancingEvaluationFixture) EvaluateCandidate(context.Context, workboard.CandidateEvaluationRequest) ([]workboard.EvidenceInput, error) {
	*e.clock = e.clock.Add(2 * time.Minute)
	return []workboard.EvidenceInput{{CriterionID: "tests", Source: "deterministic", Outcome: "passed",
		ActorID: "go-test", ActorType: "validator", Reference: "late-report"}}, nil
}

type singleBarrierEvaluationFixture struct {
	entered chan struct{}
	release chan struct{}
	calls   int
	mu      sync.Mutex
}

func (e *singleBarrierEvaluationFixture) EvaluateCandidate(ctx context.Context, _ workboard.CandidateEvaluationRequest) ([]workboard.EvidenceInput, error) {
	e.mu.Lock()
	e.calls++
	if e.calls == 1 {
		close(e.entered)
	}
	e.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-e.release:
		return []workboard.EvidenceInput{}, nil
	}
}

func (e *singleBarrierEvaluationFixture) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

func TestWorkboardCandidateEvaluationBindsBudgetedRuntimeAdmission(t *testing.T) {
	ctx := context.Background()
	store := openExecutionAdmissionStore(t, ctx)
	defer store.Close()
	clock := time.Date(2026, 9, 12, 11, 0, 0, 0, time.UTC)
	card, boardID := createReadyBudgetCard(t, ctx, store, clock, "evaluation-binding", workboardTestBudget())
	clock = card.UpdatedAt.Add(time.Second)
	event := budgetedStart("binding-worker", "binding-task", "binding-session", clock, .0005)
	reservation := executionReservation(event, 10_000, 2_000, 500, 4, 2)
	receipt, err := claimBudgetedStart(t, ctx, store, &clock, card, boardID, event, reservation, "binding-claim-key")
	if err != nil || receipt.CardRevision == nil || receipt.ClaimRevision == nil {
		t.Fatalf("claim=%+v err=%v", receipt, err)
	}
	attemptID, claimID := currentLifecycleIDs(t, store, boardID, card.ID)
	terminal := appendBudgetRuntime(t, ctx, store, event, &providers.Usage{InputTokens: 4, OutputTokens: 3}, event.Time.Add(2*time.Second), runtime.TaskCompleted)
	evaluator := &evaluationFixture{evidence: []workboard.EvidenceInput{{CriterionID: "tests", Source: "deterministic", Outcome: "passed",
		ActorID: "go-test", ActorType: "validator", Reference: "binding-report"}}}
	clock = terminal.Time.Add(time.Second)
	service := newTestEvaluationService(t, store, workboard.Actor{ID: event.WorkerID, Type: "worker"}, evaluator, &clock)
	summary, artifacts := "candidate bound to admission", []string{"task://binding-task"}
	_, err = service.SubmitCandidate(ctx, workboard.SubmitCandidateRequest{BoardID: boardID, CardID: card.ID, AttemptID: attemptID,
		ClaimID: claimID, IdempotencyKey: "binding-candidate-key", ExpectedCardRevision: *receipt.CardRevision,
		ExpectedClaimRevision: *receipt.ClaimRevision, CriteriaRevision: card.CriteriaRevision, Summary: summary, ArtifactRefs: artifacts})
	if err != nil {
		t.Fatal(err)
	}
	frozen := evaluator.frozen()
	if frozen.Validate() != nil || frozen.BindingKind != "runtime_budgeted" || frozen.SourceTaskID != event.TaskID || frozen.SourceSessionID != event.SessionID ||
		frozen.SourceTurnID != "turn-1" || frozen.SourceAttemptID != "model-attempt-1" ||
		frozen.SourceCompletionEventID != event.TaskID+"-turn-done" || frozen.SourceCompletionSequence != 3 || frozen.SourceCompletionDigest == "" ||
		frozen.SourceOutputDigest != digestBytes(nil) || frozen.SourceTerminalEventID != terminal.ID ||
		frozen.SourceTerminalSequence != terminal.Sequence || frozen.SourceTerminalDigest == "" ||
		frozen.ConfigID != reservation.ConfigID || frozen.WorkerID != event.WorkerID || frozen.CandidateDigest != workboard.CandidateContentDigest(summary, artifacts) ||
		frozen.AdmissionID == "" || frozen.AdmissionDigest == "" || frozen.SourceModelID != event.Data.ModelID ||
		frozen.SourceProviderID != event.Data.ProviderID || frozen.SourceTimeLimitMS != reservation.TimeLimitMS ||
		frozen.SourceTokenLimit != reservation.TokenLimit || frozen.SourceCostMicros != reservation.CostMicros ||
		frozen.CriteriaDigest == "" || frozen.PolicyDigest == "" || len(frozen.Criteria) != 1 {
		t.Fatalf("frozen evaluation binding mismatch: %+v", frozen)
	}
	candidate, _ := evaluationRows(t, store, boardID, card.ID, attemptID)
	if candidate.ID != frozen.CandidateID || candidate.Digest != frozen.CandidateDigest || candidate.CriteriaDigest != frozen.CriteriaDigest ||
		candidate.PolicyDigest != frozen.PolicyDigest {
		t.Fatalf("stored candidate drifted from evaluator input: candidate=%+v frozen=%+v", candidate, frozen)
	}
}

func TestWorkboardCandidateEvaluationRejectsTaskTerminalAttemptDrift(t *testing.T) {
	ctx := context.Background()
	store := openExecutionAdmissionStore(t, ctx)
	defer store.Close()
	clock := time.Date(2026, 9, 12, 11, 15, 0, 0, time.UTC)
	card, boardID := createReadyBudgetCard(t, ctx, store, clock, "terminal-attempt-drift", workboardTestBudget())
	clock = card.UpdatedAt.Add(time.Second)
	start := budgetedStart("terminal-worker", "terminal-task", "terminal-session", clock, .0005)
	receipt, err := claimBudgetedStart(t, ctx, store, &clock, card, boardID, start,
		executionReservation(start, 10_000, 2_000, 500, 4, 2), "terminal-drift-claim")
	if err != nil || receipt.CardRevision == nil || receipt.ClaimRevision == nil {
		t.Fatal(receipt, err)
	}
	attemptID, claimID := currentLifecycleIDs(t, store, boardID, card.ID)
	terminal := appendBudgetRuntime(t, ctx, store, start, &providers.Usage{InputTokens: 4, OutputTokens: 3},
		start.Time.Add(2*time.Second), runtime.TaskCompleted)
	terminal.AttemptID = "different-model-attempt"
	body, err := terminal.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`UPDATE events SET body=? WHERE id=?; UPDATE event_log SET body_digest=? WHERE event_id=?`,
		body, terminal.ID, streamBodyDigest(body), terminal.ID); err != nil {
		t.Fatal(err)
	}
	clock = terminal.Time.Add(time.Second)
	evaluator := &evaluationFixture{}
	service := newTestEvaluationService(t, store, workboard.Actor{ID: start.WorkerID, Type: "worker"}, evaluator, &clock)
	_, err = service.SubmitCandidate(ctx, workboard.SubmitCandidateRequest{BoardID: boardID, CardID: card.ID, AttemptID: attemptID,
		ClaimID: claimID, IdempotencyKey: "terminal-drift-candidate", ExpectedCardRevision: *receipt.CardRevision,
		ExpectedClaimRevision: *receipt.ClaimRevision, CriteriaRevision: card.CriteriaRevision, Summary: "must not dispatch"})
	if !errors.Is(err, ErrWorkboardCorrupt) || evaluator.count() != 0 {
		t.Fatalf("terminal attempt drift err=%v evaluator calls=%d", err, evaluator.count())
	}
}

func TestWorkboardCandidateEvaluationRequiresSuccessfulBudgetProofBeforeDispatch(t *testing.T) {
	for _, test := range []struct {
		name       string
		appendDone bool
		tokens     int64
	}{
		{name: "missing terminal"},
		{name: "token overrun", appendDone: true, tokens: 11},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store := openExecutionAdmissionStore(t, ctx)
			defer store.Close()
			clock := time.Date(2026, 9, 12, 11, 30, 0, 0, time.UTC)
			card, boardID := createReadyBudgetCard(t, ctx, store, clock, "proof-"+strings.ReplaceAll(test.name, " ", "-"), workboardTestBudget())
			clock = card.UpdatedAt.Add(time.Second)
			event := budgetedStart("proof-worker", "proof-task-"+strings.ReplaceAll(test.name, " ", "-"), "proof-session", clock, .0005)
			reservation := executionReservation(event, 10_000, 10, 500, 4, 2)
			receipt, err := claimBudgetedStart(t, ctx, store, &clock, card, boardID, event, reservation, "proof-claim-key-01")
			if err != nil {
				t.Fatal(err)
			}
			if test.appendDone {
				terminal := appendBudgetRuntime(t, ctx, store, event, &providers.Usage{OutputTokens: test.tokens}, event.Time.Add(2*time.Second), runtime.TaskCompleted)
				clock = terminal.Time.Add(time.Second)
			} else {
				clock = clock.Add(time.Second)
			}
			attemptID, claimID := currentLifecycleIDs(t, store, boardID, card.ID)
			evaluator := &evaluationFixture{}
			service := newTestEvaluationService(t, store, workboard.Actor{ID: event.WorkerID, Type: "worker"}, evaluator, &clock)
			_, err = service.SubmitCandidate(ctx, workboard.SubmitCandidateRequest{BoardID: boardID, CardID: card.ID, AttemptID: attemptID,
				ClaimID: claimID, IdempotencyKey: "proof-candidate-key", ExpectedCardRevision: *receipt.CardRevision,
				ExpectedClaimRevision: *receipt.ClaimRevision, CriteriaRevision: card.CriteriaRevision, Summary: "must not dispatch"})
			if err == nil || evaluator.count() != 0 {
				t.Fatalf("err=%v evaluator calls=%d", err, evaluator.count())
			}
		})
	}
}

func TestWorkboardCandidateEvaluationCannotCommitAfterLeaseExpiry(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	card, boardID := readyLifecycleCard(t, ctx, store, clock)
	worker := workboard.Actor{ID: "late-worker", Type: "worker"}
	clock = card.UpdatedAt.Add(time.Second)
	lifecycle := newTestLifecycleService(t, store, worker, verifiedLifecycleRecovery("late-proof", workboard.EffectFree), &clock)
	claim, err := lifecycle.Claim(ctx, workboard.ClaimRequest{BoardID: boardID, CardID: card.ID,
		IdempotencyKey: "late-claim-key-001", ExpectedCardRevision: card.Revision})
	if err != nil || claim.CardRevision == nil || claim.ClaimRevision == nil {
		t.Fatal(claim, err)
	}
	attemptID, claimID := currentLifecycleIDs(t, store, boardID, card.ID)
	service := newTestEvaluationService(t, store, worker, advancingEvaluationFixture{clock: &clock}, &clock)
	_, err = service.SubmitCandidate(ctx, workboard.SubmitCandidateRequest{BoardID: boardID, CardID: card.ID, AttemptID: attemptID,
		ClaimID: claimID, IdempotencyKey: "late-candidate-key", ExpectedCardRevision: *claim.CardRevision,
		ExpectedClaimRevision: *claim.ClaimRevision, CriteriaRevision: card.CriteriaRevision, Summary: "late candidate"})
	if !errors.Is(err, &workboard.Violation{Code: workboard.CodeLeaseExpired}) {
		t.Fatalf("expired evaluation committed or returned wrong error: %v", err)
	}
	current, getErr := store.GetCard(ctx, boardID, card.ID)
	if getErr != nil || current.State != workboard.InProgress || current.CurrentClaimID != claimID {
		t.Fatalf("candidate state changed after lease expiry: card=%+v err=%v", current, getErr)
	}
}

func TestWorkboardCandidateSameIdempotencyKeyRejectsConcurrentPayloadDriftBeforeEvaluation(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := time.Date(2026, 9, 12, 12, 30, 0, 0, time.UTC)
	card, boardID := readyLifecycleCard(t, ctx, store, clock)
	worker := workboard.Actor{ID: "coalesced-worker", Type: "worker"}
	clock = card.UpdatedAt.Add(time.Second)
	lifecycle := newTestLifecycleService(t, store, worker, verifiedLifecycleRecovery("coalesced-proof", workboard.EffectFree), &clock)
	claim, err := lifecycle.Claim(ctx, workboard.ClaimRequest{BoardID: boardID, CardID: card.ID,
		IdempotencyKey: "coalesced-claim-key", ExpectedCardRevision: card.Revision})
	if err != nil {
		t.Fatal(err)
	}
	attemptID, claimID := currentLifecycleIDs(t, store, boardID, card.ID)
	evaluator := &singleBarrierEvaluationFixture{entered: make(chan struct{}), release: make(chan struct{})}
	service := newTestEvaluationService(t, store, worker, evaluator, &clock)
	request := workboard.SubmitCandidateRequest{BoardID: boardID, CardID: card.ID, AttemptID: attemptID, ClaimID: claimID,
		IdempotencyKey: "same-candidate-key-01", ExpectedCardRevision: *claim.CardRevision, ExpectedClaimRevision: *claim.ClaimRevision,
		CriteriaRevision: card.CriteriaRevision, Summary: "first payload"}
	first := make(chan error, 1)
	go func() {
		_, callErr := service.SubmitCandidate(ctx, request)
		first <- callErr
	}()
	select {
	case <-evaluator.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first evaluator did not start")
	}
	drifted := request
	drifted.Summary = "different payload"
	if _, err = service.SubmitCandidate(ctx, drifted); !errors.Is(err, &workboard.Violation{Code: workboard.CodeInvalid}) {
		t.Fatalf("concurrent payload drift was not rejected: %v", err)
	}
	if evaluator.count() != 1 {
		t.Fatalf("payload drift dispatched %d evaluators", evaluator.count())
	}
	close(evaluator.release)
	if err = <-first; err != nil {
		t.Fatal(err)
	}
}

func TestWorkboardCandidateCommitSamplesClockAfterWriterWait(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := time.Date(2026, 9, 12, 13, 0, 0, 0, time.UTC)
	card, boardID := readyLifecycleCard(t, ctx, store, clock)
	worker := workboard.Actor{ID: "writer-wait-worker", Type: "worker"}
	clock = card.UpdatedAt.Add(time.Second)
	lifecycle := newTestLifecycleService(t, store, worker, verifiedLifecycleRecovery("writer-wait-proof", workboard.EffectFree), &clock)
	claim, err := lifecycle.Claim(ctx, workboard.ClaimRequest{BoardID: boardID, CardID: card.ID,
		IdempotencyKey: "writer-wait-claim-key", ExpectedCardRevision: card.Revision})
	if err != nil {
		t.Fatal(err)
	}
	attemptID, claimID := currentLifecycleIDs(t, store, boardID, card.ID)
	evaluator := &singleBarrierEvaluationFixture{entered: make(chan struct{}), release: make(chan struct{})}
	var clockCalls atomic.Int32
	service, err := workboard.NewEvaluationService(store, telemetryCardAuthority{authority: workboard.Authority{
		CreationScope: "acceptance-authority", Actor: worker}}, evaluator, func() time.Time {
		if clockCalls.Add(1) >= 3 {
			return clock.Add(2 * time.Minute)
		}
		return clock
	})
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, callErr := service.SubmitCandidate(ctx, workboard.SubmitCandidateRequest{BoardID: boardID, CardID: card.ID,
			AttemptID: attemptID, ClaimID: claimID, IdempotencyKey: "writer-wait-submit-key", ExpectedCardRevision: *claim.CardRevision,
			ExpectedClaimRevision: *claim.ClaimRevision, CriteriaRevision: card.CriteriaRevision, Summary: "writer-wait candidate"})
		result <- callErr
	}()
	select {
	case <-evaluator.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("evaluator did not enter")
	}
	blocker, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = reserveWorkboardWriter(ctx, blocker); err != nil {
		_ = blocker.Rollback()
		t.Fatal(err)
	}
	close(evaluator.release)
	if err = blocker.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err = <-result; !errors.Is(err, &workboard.Violation{Code: workboard.CodeLeaseExpired}) {
		t.Fatalf("writer wait reused stale lease time: %v", err)
	}
}
