package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

func appendBudgetRuntime(t *testing.T, ctx context.Context, store *Store, start runtime.Event, usage *providers.Usage, terminalAt time.Time, kind runtime.Kind) runtime.Event {
	t.Helper()
	turn := runtime.Event{Version: 1, ID: start.TaskID + "-turn", TaskID: start.TaskID, SessionID: start.SessionID,
		CorrelationID: start.TaskID, TurnID: "turn-1", AttemptID: "model-attempt-1", WorkerID: start.WorkerID,
		Sequence: 2, Time: start.Time.Add(time.Second), Kind: runtime.TurnStarted,
		Data: runtime.Data{ModelID: start.Data.ModelID, ProviderID: start.Data.ProviderID}}
	done := runtime.Event{Version: 1, ID: start.TaskID + "-turn-done", TaskID: start.TaskID, SessionID: start.SessionID,
		CorrelationID: start.TaskID, TurnID: turn.TurnID, AttemptID: turn.AttemptID, WorkerID: start.WorkerID,
		Sequence: 3, Time: start.Time.Add(1500 * time.Millisecond), Kind: runtime.TurnCompleted,
		Data: runtime.Data{ModelID: start.Data.ModelID, ProviderID: start.Data.ProviderID, Usage: usage}}
	terminal := runtime.Event{Version: 1, ID: start.TaskID + "-terminal", TaskID: start.TaskID, SessionID: start.SessionID,
		CorrelationID: start.TaskID, WorkerID: start.WorkerID, Sequence: 4, Time: terminalAt.UTC(), Kind: kind}
	for expected, event := range []runtime.Event{turn, done, terminal} {
		if err := store.Append(ctx, int64(expected+1), event); err != nil {
			t.Fatal(err)
		}
	}
	return terminal
}

func submitBudgetCandidate(t *testing.T, ctx context.Context, store *Store, clock *time.Time, card workboard.Card, boardID, workerID, attemptID, claimID, key string) (workboard.OperationReceipt, error) {
	return submitBudgetCandidateWithOutcome(t, ctx, store, clock, card, boardID, workerID, attemptID, claimID, key, "passed")
}

func submitBudgetCandidateWithOutcome(t *testing.T, ctx context.Context, store *Store, clock *time.Time, card workboard.Card, boardID, workerID, attemptID, claimID, key, outcome string) (workboard.OperationReceipt, error) {
	t.Helper()
	evaluator := &evaluationFixture{evidence: []workboard.EvidenceInput{{CriterionID: "tests", Source: "deterministic", Outcome: outcome,
		ActorID: "go-test", ActorType: "validator", Reference: "test-report"}}}
	service := newTestEvaluationService(t, store, workboard.Actor{ID: workerID, Type: "worker"}, evaluator, clock)
	return service.SubmitCandidate(ctx, workboard.SubmitCandidateRequest{BoardID: boardID, CardID: card.ID, AttemptID: attemptID,
		ClaimID: claimID, IdempotencyKey: key, ExpectedCardRevision: card.Revision + 1, ExpectedClaimRevision: 1,
		CriteriaRevision: card.CriteriaRevision, Summary: "proof-bearing candidate", ArtifactRefs: []string{}})
}

func TestExecutionSettlementKnownUsageExactReplayAndTamperFence(t *testing.T) {
	ctx := context.Background()
	store := openExecutionAdmissionStore(t, ctx)
	defer store.Close()
	clock := time.Date(2026, 9, 10, 16, 0, 0, 0, time.UTC)
	card, boardID := createReadyBudgetCard(t, ctx, store, clock, "known", workboardTestBudget())
	clock = card.UpdatedAt.Add(time.Second)
	start := budgetedStart("settle-worker", "settle-task", "settle-session", clock, .0005)
	reservation := executionReservation(start, 3_000, 1_000, 500, 2, 1)
	if _, err := claimBudgetedStart(t, ctx, store, &clock, card, boardID, start, reservation, "settle-known-claim"); err != nil {
		t.Fatal(err)
	}
	attemptID, claimID := currentLifecycleIDs(t, store, boardID, card.ID)
	terminal := appendBudgetRuntime(t, ctx, store, start, &providers.Usage{InputTokens: 7, OutputTokens: 5}, start.Time.Add(2000*time.Millisecond+time.Nanosecond), runtime.TaskCompleted)
	clock = terminal.Time.Add(time.Second)
	receipt, err := submitBudgetCandidate(t, ctx, store, &clock, card, boardID, start.WorkerID, attemptID, claimID, "settle-known-submit")
	if err != nil {
		t.Fatal(err)
	}
	admissionID := executionAdmissionID(t, store, start.TaskID)
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	indexed, canonical, found, err := readExecutionSettlement(ctx, tx, admissionID)
	_ = tx.Rollback()
	if err != nil || !found || indexed != canonical || indexed.ChargedTimeMS != 2001 || indexed.ChargedTokens != 12 ||
		indexed.ChargedCostMicros != 500 || !indexed.TokenUsageKnown || indexed.TerminalEventID != terminal.ID || !indexed.SettledAt.Equal(clock) {
		t.Fatalf("settlement=%+v canonical=%+v found=%t err=%v", indexed, canonical, found, err)
	}
	clock = clock.Add(time.Hour)
	replayed, err := submitBudgetCandidate(t, ctx, store, &clock, card, boardID, start.WorkerID, attemptID, claimID, "settle-known-submit")
	if err != nil || replayed.OperationID != receipt.OperationID {
		t.Fatalf("replay=%+v want=%+v err=%v", replayed, receipt, err)
	}
	forged := indexed
	forged.ChargedTokens = 1
	forged.SettlementDigest, _ = forged.CanonicalDigest()
	forgedBody, _ := json.Marshal(forged)
	if _, err = store.db.Exec(`DROP TRIGGER workboard_execution_settlement_immutable_update;
		UPDATE workboard_execution_settlements SET charged_tokens=?,settlement_digest=?,body=? WHERE task_id=?`,
		forged.ChargedTokens, forged.SettlementDigest, forgedBody, start.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err = submitBudgetCandidate(t, ctx, store, &clock, card, boardID, start.WorkerID, attemptID, claimID, "settle-known-submit"); !errors.Is(err, ErrWorkboardCorrupt) {
		t.Fatalf("tampered settlement replay=%v", err)
	}
	nextCard, nextBoard := createReadyBudgetCard(t, ctx, store, time.Date(2026, 9, 10, 15, 0, 0, 0, time.UTC), "after-tamper", workboardTestBudget())
	nextClock := nextCard.UpdatedAt.Add(time.Second)
	nextStart := budgetedStart("next-worker", "next-task", "next-session", nextClock, .0005)
	if _, err = claimBudgetedStart(t, ctx, store, &nextClock, nextCard, nextBoard, nextStart,
		executionReservation(nextStart, 1_000, 100, 500, 2, 1), "next-budget-claim"); !errors.Is(err, ErrWorkboardCorrupt) {
		t.Fatalf("forged under-count opened new capacity: %v", err)
	}
}

func TestExecutionSettlementCandidateReplayAfterDecision(t *testing.T) {
	ctx := context.Background()
	for _, decisionKind := range []string{"accepted", "rejected"} {
		t.Run(decisionKind, func(t *testing.T) {
			store := openExecutionAdmissionStore(t, ctx)
			defer store.Close()
			clock := time.Date(2026, 9, 10, 14, 0, 0, 0, time.UTC)
			card, boardID := createReadyBudgetCard(t, ctx, store, clock, "post-"+decisionKind, workboardTestBudget())
			clock = card.UpdatedAt.Add(time.Second)
			start := budgetedStart("decision-worker", "decision-"+decisionKind+"-task", "decision-session", clock, .0005)
			if _, err := claimBudgetedStart(t, ctx, store, &clock, card, boardID, start,
				executionReservation(start, 5_000, 100, 500, 2, 1), "decision-"+decisionKind+"-claim"); err != nil {
				t.Fatal(err)
			}
			attemptID, claimID := currentLifecycleIDs(t, store, boardID, card.ID)
			terminal := appendBudgetRuntime(t, ctx, store, start, &providers.Usage{InputTokens: 4, OutputTokens: 3}, start.Time.Add(2*time.Second), runtime.TaskCompleted)
			clock = terminal.Time.Add(time.Second)
			outcome := "passed"
			if decisionKind == "rejected" {
				outcome = "failed"
			}
			submitted, err := submitBudgetCandidateWithOutcome(t, ctx, store, &clock, card, boardID, start.WorkerID, attemptID, claimID, "decision-"+decisionKind+"-submit", outcome)
			if err != nil {
				t.Fatal(err)
			}
			candidate, evidence := evaluationRows(t, store, boardID, card.ID, attemptID)
			current, err := store.GetCard(ctx, boardID, card.ID)
			if err != nil {
				t.Fatal(err)
			}
			clock = clock.Add(time.Second)
			operator := newTestEvaluationService(t, store, workboard.Actor{ID: "decision-operator", Type: "operator"}, &evaluationFixture{}, &clock)
			decision := workboard.DecideCandidateRequest{BoardID: boardID, CardID: card.ID, AttemptID: attemptID, CandidateID: candidate.ID,
				IdempotencyKey: "decision-" + decisionKind + "-review", ExpectedCardRevision: current.Revision, CriteriaRevision: card.CriteriaRevision,
				CandidateDigest: candidate.Digest, CriteriaDigest: candidate.CriteriaDigest, EvidenceHeadRevision: int64(len(evidence)),
				EvidenceSetDigest: workboard.EvidenceSetDigest(evidence), PolicyDigest: candidate.PolicyDigest, Evidence: "independent decision"}
			if decisionKind == "accepted" {
				_, err = operator.AcceptCandidate(ctx, decision)
			} else {
				_, err = operator.RejectCandidate(ctx, decision)
			}
			if err != nil {
				t.Fatal(err)
			}
			clock = clock.Add(time.Hour)
			replayed, err := submitBudgetCandidateWithOutcome(t, ctx, store, &clock, card, boardID, start.WorkerID, attemptID, claimID, "decision-"+decisionKind+"-submit", outcome)
			if err != nil || replayed.OperationID != submitted.OperationID {
				t.Fatalf("post-%s replay=%+v want=%+v err=%v", decisionKind, replayed, submitted, err)
			}
		})
	}
}

func TestExecutionSettlementUnknownUsageAndInvalidTimeChargeReservation(t *testing.T) {
	ctx := context.Background()
	store := openExecutionAdmissionStore(t, ctx)
	defer store.Close()
	clock := time.Date(2026, 9, 10, 16, 15, 0, 0, time.UTC)
	card, boardID := createReadyBudgetCard(t, ctx, store, clock, "unknown", workboardTestBudget())
	clock = card.UpdatedAt.Add(time.Second)
	start := budgetedStart("unknown-worker", "unknown-task", "unknown-session", clock, .0007)
	reservation := executionReservation(start, 3_000, 900, 700, 2, 1)
	claimReceipt, err := claimBudgetedStart(t, ctx, store, &clock, card, boardID, start, reservation, "settle-unknown-claim")
	if err != nil {
		t.Fatal(err)
	}
	attemptID, claimID := currentLifecycleIDs(t, store, boardID, card.ID)
	terminal := runtime.Event{Version: 1, ID: start.TaskID + "-terminal", TaskID: start.TaskID, SessionID: start.SessionID,
		CorrelationID: start.TaskID, WorkerID: start.WorkerID, Sequence: 2, Time: start.Time.Add(-time.Second), Kind: runtime.TaskFailed,
		Data: runtime.Data{Code: "provider_retryable_no_output"}}
	if err = store.Append(ctx, 1, terminal); err != nil {
		t.Fatal(err)
	}
	clock = start.Time.Add(time.Second)
	lifecycle := newTestLifecycleService(t, store, workboard.Actor{ID: start.WorkerID, Type: "worker"},
		verifiedLifecycleRecovery("unused-settlement-proof", workboard.EffectFree), &clock)
	failure, err := lifecycle.Fail(ctx, workboard.FailClaimRequest{BoardID: boardID, CardID: card.ID, AttemptID: attemptID, ClaimID: claimID,
		IdempotencyKey: "settle-unknown-fail", ExpectedCardRevision: *claimReceipt.CardRevision,
		ExpectedClaimRevision: *claimReceipt.ClaimRevision, EffectResolution: workboard.EffectFree})
	if err != nil {
		t.Fatal(err)
	}
	settlement := executionSettlementByTask(t, store, start.TaskID)
	if settlement.ChargedTimeMS != reservation.TimeLimitMS || settlement.ChargedTokens != reservation.TokenLimit ||
		settlement.ChargedCostMicros != reservation.CostMicros || settlement.TokenUsageKnown {
		t.Fatalf("conservative settlement=%+v", settlement)
	}
	clock = clock.Add(time.Hour)
	replayed, err := lifecycle.Fail(ctx, workboard.FailClaimRequest{BoardID: boardID, CardID: card.ID, AttemptID: attemptID, ClaimID: claimID,
		IdempotencyKey: "settle-unknown-fail", ExpectedCardRevision: *claimReceipt.CardRevision,
		ExpectedClaimRevision: *claimReceipt.ClaimRevision, EffectResolution: workboard.EffectFree})
	if err != nil || replayed.OperationID != failure.OperationID {
		t.Fatalf("failure replay=%+v want=%+v err=%v", replayed, failure, err)
	}
}

func TestExecutionSettlementFailureRollsBackAttemptFinalization(t *testing.T) {
	ctx := context.Background()
	store := openExecutionAdmissionStore(t, ctx)
	defer store.Close()
	clock := time.Date(2026, 9, 10, 16, 30, 0, 0, time.UTC)
	card, boardID := createReadyBudgetCard(t, ctx, store, clock, "rollback", workboardTestBudget())
	clock = card.UpdatedAt.Add(time.Second)
	start := budgetedStart("rollback-settle-worker", "rollback-settle-task", "rollback-settle-session", clock, .0005)
	if _, err := claimBudgetedStart(t, ctx, store, &clock, card, boardID, start,
		executionReservation(start, 10_000, 1_000, 500, 2, 1), "settle-rollback-claim"); err != nil {
		t.Fatal(err)
	}
	attemptID, claimID := currentLifecycleIDs(t, store, boardID, card.ID)
	terminal := appendBudgetRuntime(t, ctx, store, start, &providers.Usage{InputTokens: 1, OutputTokens: 1}, start.Time.Add(2*time.Second), runtime.TaskCompleted)
	clock = terminal.Time.Add(time.Second)
	if _, err := store.db.Exec(`CREATE TRIGGER reject_execution_settlement BEFORE INSERT ON workboard_execution_settlements
		BEGIN SELECT RAISE(ABORT,'reject settlement'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := submitBudgetCandidate(t, ctx, store, &clock, card, boardID, start.WorkerID, attemptID, claimID, "settle-rollback-submit"); err == nil {
		t.Fatal("settlement rejection allowed candidate finalization")
	}
	current, err := store.GetCard(ctx, boardID, card.ID)
	var attemptState, claimState string
	var candidates, settlements int
	if err != nil || current.State != workboard.InProgress || store.db.QueryRow(`SELECT state FROM workboard_attempts WHERE id=?`, attemptID).Scan(&attemptState) != nil ||
		store.db.QueryRow(`SELECT state FROM workboard_claims WHERE id=?`, claimID).Scan(&claimState) != nil ||
		store.db.QueryRow(`SELECT count(*) FROM workboard_candidates WHERE attempt_id=?`, attemptID).Scan(&candidates) != nil ||
		store.db.QueryRow(`SELECT count(*) FROM workboard_execution_settlements WHERE task_id=?`, start.TaskID).Scan(&settlements) != nil ||
		attemptState != "running" || claimState != "active" || candidates != 0 || settlements != 0 {
		t.Fatalf("card=%+v attempt=%s claim=%s candidates=%d settlements=%d err=%v", current, attemptState, claimState, candidates, settlements, err)
	}
	if _, err = store.db.Exec(`DROP TRIGGER reject_execution_settlement`); err != nil {
		t.Fatal(err)
	}
	if _, err = submitBudgetCandidate(t, ctx, store, &clock, card, boardID, start.WorkerID, attemptID, claimID, "settle-rollback-submit"); err != nil {
		t.Fatal(err)
	}
}

func TestExecutionSettlementRejectsCanonicalJournalIdentityTamper(t *testing.T) {
	ctx := context.Background()
	store := openExecutionAdmissionStore(t, ctx)
	defer store.Close()
	clock := time.Date(2026, 9, 10, 16, 45, 0, 0, time.UTC)
	card, boardID := createReadyBudgetCard(t, ctx, store, clock, "identity", workboardTestBudget())
	clock = card.UpdatedAt.Add(time.Second)
	start := budgetedStart("identity-worker", "identity-task", "identity-session", clock, .0005)
	if _, err := claimBudgetedStart(t, ctx, store, &clock, card, boardID, start,
		executionReservation(start, 10_000, 1_000, 500, 2, 1), "identity-budget-claim"); err != nil {
		t.Fatal(err)
	}
	attemptID, claimID := currentLifecycleIDs(t, store, boardID, card.ID)
	terminal := appendBudgetRuntime(t, ctx, store, start, &providers.Usage{InputTokens: 2, OutputTokens: 3}, start.Time.Add(2*time.Second), runtime.TaskCompleted)
	events, err := store.Read(ctx, start.TaskID, 0, 10)
	if err != nil || len(events) != 4 {
		t.Fatal(events, err)
	}
	tampered := events[1]
	tampered.WorkerID = "other-worker"
	body, err := tampered.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`UPDATE events SET body=? WHERE id=?; UPDATE event_log SET body_digest=? WHERE event_id=?`,
		body, tampered.ID, streamBodyDigest(body), tampered.ID); err != nil {
		t.Fatal(err)
	}
	clock = terminal.Time.Add(time.Second)
	if _, err = submitBudgetCandidate(t, ctx, store, &clock, card, boardID, start.WorkerID, attemptID, claimID, "identity-submit-01"); !errors.Is(err, ErrWorkboardCorrupt) {
		t.Fatalf("canonical identity tamper accepted: %v", err)
	}
	var settlements int
	current, readErr := store.GetCard(ctx, boardID, card.ID)
	if queryErr := store.db.QueryRow(`SELECT count(*) FROM workboard_execution_settlements WHERE task_id=?`, start.TaskID).Scan(&settlements); queryErr != nil ||
		readErr != nil || settlements != 0 || current.State != workboard.InProgress {
		t.Fatalf("settlements=%d card=%+v errors=%v/%v", settlements, current, queryErr, readErr)
	}
}

func TestExecutionSettlementRejectsTerminalKindMismatchAtomically(t *testing.T) {
	ctx := context.Background()
	store := openExecutionAdmissionStore(t, ctx)
	defer store.Close()
	clock := time.Date(2026, 9, 10, 17, 0, 0, 0, time.UTC)
	card, boardID := createReadyBudgetCard(t, ctx, store, clock, "kind", workboardTestBudget())
	clock = card.UpdatedAt.Add(time.Second)
	start := budgetedStart("kind-worker", "kind-task", "kind-session", clock, .0005)
	if _, err := claimBudgetedStart(t, ctx, store, &clock, card, boardID, start,
		executionReservation(start, 10_000, 1_000, 500, 2, 1), "kind-budget-claim"); err != nil {
		t.Fatal(err)
	}
	attemptID, claimID := currentLifecycleIDs(t, store, boardID, card.ID)
	terminal := appendBudgetRuntime(t, ctx, store, start, &providers.Usage{InputTokens: 2, OutputTokens: 3}, start.Time.Add(2*time.Second), runtime.TaskFailed)
	clock = terminal.Time.Add(time.Second)
	if _, err := submitBudgetCandidate(t, ctx, store, &clock, card, boardID, start.WorkerID, attemptID, claimID, "kind-mismatch-submit"); !errors.Is(err, ErrWorkboardCorrupt) {
		t.Fatalf("failed runtime submitted a candidate: %v", err)
	}
	var attemptState, claimState string
	var settlements int
	current, readErr := store.GetCard(ctx, boardID, card.ID)
	if queryErr := store.db.QueryRow(`SELECT state FROM workboard_attempts WHERE id=?`, attemptID).Scan(&attemptState); queryErr != nil {
		t.Fatal(queryErr)
	}
	if queryErr := store.db.QueryRow(`SELECT state FROM workboard_claims WHERE id=?`, claimID).Scan(&claimState); queryErr != nil {
		t.Fatal(queryErr)
	}
	if queryErr := store.db.QueryRow(`SELECT count(*) FROM workboard_execution_settlements WHERE task_id=?`, start.TaskID).Scan(&settlements); queryErr != nil ||
		readErr != nil || settlements != 0 || current.State != workboard.InProgress || attemptState != "running" || claimState != "active" {
		t.Fatalf("settlements=%d card=%+v attempt=%s claim=%s errors=%v/%v", settlements, current, attemptState, claimState, queryErr, readErr)
	}
}

func TestExecutionSettlementRejectsSuccessfulReservationOverrunAtomically(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name              string
		timeLimit, tokens int64
		usage             providers.Usage
		wantField         string
	}{
		{name: "time", timeLimit: 1_000, tokens: 100, usage: providers.Usage{InputTokens: 2, OutputTokens: 3}, wantField: "time_budget"},
		{name: "tokens", timeLimit: 5_000, tokens: 10, usage: providers.Usage{InputTokens: 7, OutputTokens: 5}, wantField: "token_budget"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := openExecutionAdmissionStore(t, ctx)
			defer store.Close()
			clock := time.Date(2026, 9, 10, 15, 15, 0, 0, time.UTC)
			card, boardID := createReadyBudgetCard(t, ctx, store, clock, "overrun-"+tc.name, workboardTestBudget())
			clock = card.UpdatedAt.Add(time.Second)
			start := budgetedStart("overrun-worker", "overrun-"+tc.name+"-task", "overrun-session", clock, .0005)
			if _, err := claimBudgetedStart(t, ctx, store, &clock, card, boardID, start,
				executionReservation(start, tc.timeLimit, tc.tokens, 500, 2, 1), "overrun-"+tc.name+"-claim"); err != nil {
				t.Fatal(err)
			}
			attemptID, claimID := currentLifecycleIDs(t, store, boardID, card.ID)
			terminal := appendBudgetRuntime(t, ctx, store, start, &tc.usage, start.Time.Add(2*time.Second+time.Nanosecond), runtime.TaskCompleted)
			clock = terminal.Time.Add(time.Second)
			_, err := submitBudgetCandidate(t, ctx, store, &clock, card, boardID, start.WorkerID, attemptID, claimID, "overrun-"+tc.name+"-submit")
			if !errors.Is(err, &workboard.Violation{Code: workboard.CodeLimitExceeded, Field: tc.wantField}) {
				t.Fatalf("successful overrun=%v want field=%s", err, tc.wantField)
			}
			var attemptState, claimState string
			var settlements int
			current, readErr := store.GetCard(ctx, boardID, card.ID)
			if queryErr := store.db.QueryRow(`SELECT state FROM workboard_attempts WHERE id=?`, attemptID).Scan(&attemptState); queryErr != nil {
				t.Fatal(queryErr)
			}
			if queryErr := store.db.QueryRow(`SELECT state FROM workboard_claims WHERE id=?`, claimID).Scan(&claimState); queryErr != nil {
				t.Fatal(queryErr)
			}
			if queryErr := store.db.QueryRow(`SELECT count(*) FROM workboard_execution_settlements WHERE task_id=?`, start.TaskID).Scan(&settlements); queryErr != nil ||
				readErr != nil || settlements != 0 || current.State != workboard.InProgress || attemptState != "running" || claimState != "active" {
				t.Fatalf("settlements=%d card=%+v attempt=%s claim=%s errors=%v/%v", settlements, current, attemptState, claimState, queryErr, readErr)
			}
		})
	}
}

func TestExecutionSettlementRecoveryChargesOverrunAndExhaustsCard(t *testing.T) {
	ctx := context.Background()
	store := openExecutionAdmissionStore(t, ctx)
	defer store.Close()
	clock := time.Date(2026, 9, 10, 15, 30, 0, 0, time.UTC)
	budget := workboard.WorkBudget{AttemptLimit: 2, TimeLimitMS: 1_000, TokenLimit: 10, CostMicros: 1_000}
	card, boardID := createReadyBudgetCard(t, ctx, store, clock, "recover-overrun", budget)
	clock = card.UpdatedAt.Add(time.Second)
	start := budgetedStart("recover-overrun-worker", "recover-overrun-task", "recover-overrun-session", clock, .0005)
	claimed, err := claimBudgetedStart(t, ctx, store, &clock, card, boardID, start,
		executionReservation(start, 1_000, 10, 500, 2, 1), "recover-overrun-claim")
	if err != nil {
		t.Fatal(err)
	}
	attemptID, claimID := currentLifecycleIDs(t, store, boardID, card.ID)
	terminal := appendBudgetRuntime(t, ctx, store, start, &providers.Usage{InputTokens: 15, OutputTokens: 10}, start.Time.Add(2*time.Second), runtime.TaskFailed)
	clock = terminal.Time.Add(2 * time.Minute)
	verifier := verifiedLifecycleRecovery("recover-overrun-proof", workboard.EffectFree)
	supervisor := newTestLifecycleService(t, store, workboard.Actor{ID: "recover-overrun-supervisor", Type: "system"}, verifier, &clock)
	recovered, err := supervisor.Recover(ctx, workboard.RecoverClaimRequest{BoardID: boardID, CardID: card.ID, AttemptID: attemptID, ClaimID: claimID,
		IdempotencyKey: "recover-overrun-operation", ExpectedCardRevision: *claimed.CardRevision,
		ExpectedClaimRevision: *claimed.ClaimRevision, Proof: recoveryIntent(verifier.proof)})
	if err != nil {
		t.Fatal(err)
	}
	settlement := executionSettlementByTask(t, store, start.TaskID)
	if settlement.ChargedTimeMS != 2_000 || settlement.ChargedTokens != 25 || settlement.ChargedCostMicros != 500 || !settlement.TokenUsageKnown {
		t.Fatalf("recovery overrun settlement=%+v", settlement)
	}
	current, err := store.GetCard(ctx, boardID, card.ID)
	if err != nil || current.State != workboard.Ready || recovered.CardRevision == nil || current.Revision != *recovered.CardRevision {
		t.Fatalf("recovered card=%+v receipt=%+v err=%v", current, recovered, err)
	}
	clock = clock.Add(time.Second)
	next := budgetedStart("recover-next-worker", "recover-next-task", "recover-next-session", clock, .0001)
	if _, err = claimBudgetedStart(t, ctx, store, &clock, current, boardID, next,
		executionReservation(next, 1_000, 10, 100, 2, 1), "recover-next-budget-claim"); !errors.Is(err, &workboard.Violation{Code: workboard.CodeLimitExceeded}) {
		t.Fatalf("exhausted card admitted another execution: %v", err)
	}
}

func executionAdmissionID(t *testing.T, store *Store, taskID string) string {
	t.Helper()
	var id string
	if err := store.db.QueryRow(`SELECT admission_id FROM workboard_execution_admissions WHERE task_id=?`, taskID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func executionSettlementByTask(t *testing.T, store *Store, taskID string) workboard.ExecutionSettlementRecord {
	t.Helper()
	admissionID := executionAdmissionID(t, store, taskID)
	tx, err := store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	indexed, canonical, found, err := readExecutionSettlement(context.Background(), tx, admissionID)
	if err != nil || !found || indexed != canonical {
		t.Fatalf("settlement=%+v canonical=%+v found=%t err=%v", indexed, canonical, found, err)
	}
	return canonical
}
