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

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

type frozenReviewEvaluator struct {
	request chan workboard.CandidateEvaluationRequest
	release chan struct{}
}

func (e *frozenReviewEvaluator) EvaluateCandidate(ctx context.Context, request workboard.CandidateEvaluationRequest) ([]workboard.EvidenceInput, error) {
	select {
	case e.request <- request:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case <-e.release:
		return []workboard.EvidenceInput{}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func prepareAuxiliaryReviewCandidate(t *testing.T, ctx context.Context, store *Store, suffix string) (workboard.CandidateEvaluationRequest, workboard.AuxiliaryReviewReservation, <-chan error, func()) {
	t.Helper()
	clock := time.Date(2026, 9, 12, 15, 0, 0, 0, time.UTC)
	card, boardID := createReadyBudgetCard(t, ctx, store, clock, "auxiliary-"+suffix, workboardTestBudget())
	clock = card.UpdatedAt.Add(time.Second)
	event := budgetedStart("auxiliary-worker-"+suffix, "auxiliary-task-"+suffix, "auxiliary-session-"+suffix, clock, .0005)
	execution := executionReservation(event, 10_000, 2_000, 500, 4, 2)
	receipt, err := claimBudgetedStart(t, ctx, store, &clock, card, boardID, event, execution, "auxiliary-claim-"+suffix)
	if err != nil || receipt.CardRevision == nil || receipt.ClaimRevision == nil {
		t.Fatal("claim", receipt, err)
	}
	attemptID, claimID := currentLifecycleIDs(t, store, boardID, card.ID)
	terminal := appendBudgetRuntime(t, ctx, store, event, &providers.Usage{InputTokens: 4, OutputTokens: 3}, event.Time.Add(2*time.Second), runtime.TaskCompleted)
	clock = terminal.Time.Add(time.Second)
	evaluator := &frozenReviewEvaluator{request: make(chan workboard.CandidateEvaluationRequest, 1), release: make(chan struct{})}
	service := newTestEvaluationService(t, store, workboard.Actor{ID: event.WorkerID, Type: "worker"}, evaluator, &clock)
	done := make(chan error, 1)
	go func() {
		_, submitErr := service.SubmitCandidate(ctx, workboard.SubmitCandidateRequest{BoardID: boardID, CardID: card.ID,
			AttemptID: attemptID, ClaimID: claimID, IdempotencyKey: "auxiliary-candidate-" + suffix,
			ExpectedCardRevision: *receipt.CardRevision, ExpectedClaimRevision: *receipt.ClaimRevision,
			CriteriaRevision: card.CriteriaRevision, Summary: "candidate for bounded auxiliary review", ArtifactRefs: []string{}})
		done <- submitErr
	}()
	var frozen workboard.CandidateEvaluationRequest
	select {
	case frozen = <-evaluator.request:
	case <-time.After(5 * time.Second):
		t.Fatal("candidate evaluator did not receive frozen request")
	}
	reservation := workboard.AuxiliaryReviewReservation{Version: 1, BoardID: frozen.BoardID, CardID: frozen.CardID,
		AttemptID: frozen.AttemptID, ClaimID: frozen.ClaimID, CandidateID: frozen.CandidateID,
		CandidateDigest: frozen.CandidateDigest, CriteriaDigest: frozen.CriteriaDigest, PolicyDigest: frozen.PolicyDigest,
		ReviewerID: "independent-reviewer", ModelID: "review-model", ProviderID: "review-provider",
		ConfigID: strings.Repeat("e", 64), TimeLimitMS: 5_000, TokenLimit: 1_000, CostMicros: 400}
	return frozen, reservation, done, func() { close(evaluator.release) }
}

func TestAuxiliaryReviewAdmissionAndSettlementReplay(t *testing.T) {
	ctx := context.Background()
	store := openExecutionAdmissionStore(t, ctx)
	defer store.Close()
	frozen, reservation, submitted, release := prepareAuxiliaryReviewCandidate(t, ctx, store, "normal")
	admissionTime := time.Date(2026, 9, 12, 15, 0, 4, 0, time.UTC)
	admission, created, err := store.AdmitAuxiliaryReview(ctx, frozen, reservation, "auxiliary-operation-normal", func() time.Time { return admissionTime })
	if err != nil || !created || admission.Validate() != nil || !admission.AdmittedAt.Equal(admissionTime) {
		t.Fatalf("admission=%+v created=%v err=%v", admission, created, err)
	}
	replayed, created, err := store.AdmitAuxiliaryReview(ctx, frozen, reservation, "auxiliary-operation-normal", func() time.Time {
		panic("exact replay must not resample admission time")
	})
	if err != nil || created || replayed != admission {
		t.Fatalf("replay=%+v created=%v err=%v", replayed, created, err)
	}
	drifted := reservation
	drifted.TokenLimit++
	if _, _, err = store.AdmitAuxiliaryReview(ctx, frozen, drifted, "auxiliary-operation-normal", time.Now); !errors.Is(err, ErrConflict) {
		t.Fatalf("operation drift accepted: %v", err)
	}
	release()
	if err = <-submitted; err != nil {
		t.Fatal("candidate commit", err)
	}
	timeMS, tokens := int64(25), int64(12)
	settledAt := admissionTime.Add(time.Second)
	if _, _, err = store.SettleAuxiliaryReview(ctx, admission.OperationID, workboard.AuxiliaryReviewCompleted,
		AuxiliaryReviewMeasurements{TimeMS: &timeMS, Tokens: &tokens}, settledAt); !errors.Is(err, ErrConflict) {
		t.Fatalf("post-schema-45 review completed without atomic outcome: %v", err)
	}
	settlement, settled, err := store.SettleAuxiliaryReview(ctx, admission.OperationID, workboard.AuxiliaryReviewFailed,
		AuxiliaryReviewMeasurements{TimeMS: &timeMS, Tokens: &tokens}, settledAt)
	if err != nil || !settled || settlement.Validate() != nil || settlement.TimeChargeMode != workboard.AuxiliaryReviewMeasured ||
		settlement.TokenChargeMode != workboard.AuxiliaryReviewMeasured || settlement.CostChargeMode != workboard.AuxiliaryReviewConservative ||
		settlement.ChargedCostMicros != admission.CostMicros {
		t.Fatalf("settlement=%+v settled=%v err=%v", settlement, settled, err)
	}
	replayedSettlement, settled, err := store.SettleAuxiliaryReview(ctx, admission.OperationID, workboard.AuxiliaryReviewFailed,
		AuxiliaryReviewMeasurements{TimeMS: &timeMS, Tokens: &tokens}, settledAt.Add(time.Minute))
	if err != nil || settled || replayedSettlement != settlement {
		t.Fatalf("settlement replay=%+v settled=%v err=%v", replayedSettlement, settled, err)
	}
	if _, _, err = store.SettleAuxiliaryReview(ctx, admission.OperationID, workboard.AuxiliaryReviewCompleted,
		AuxiliaryReviewMeasurements{}, settledAt); !errors.Is(err, ErrConflict) {
		t.Fatalf("terminal drift accepted: %v", err)
	}
}

func TestAuxiliaryReviewRestartReplayHasNoDispatchAuthorityAndSettlesConservatively(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "auxiliary-review-restart.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	frozen, reservation, submitted, release := prepareAuxiliaryReviewCandidate(t, ctx, store, "restart")
	admissionTime := time.Date(2026, 9, 12, 15, 0, 4, 0, time.UTC)
	admission, created, err := store.AdmitAuxiliaryReview(ctx, frozen, reservation, "auxiliary-operation-restart", func() time.Time {
		return admissionTime
	})
	if err != nil || !created {
		t.Fatalf("initial admission=%+v created=%v err=%v", admission, created, err)
	}
	// Model dispatch has happened once after this durable pre-dispatch fact. The
	// candidate may commit, but the process crashes before terminal accounting.
	release()
	if err = <-submitted; err != nil {
		t.Fatal("candidate commit", err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restarted.Close() }()
	replayed, dispatchAuthorized, err := restarted.AdmitAuxiliaryReview(ctx, frozen, reservation, admission.OperationID, func() time.Time {
		panic("durable replay must neither resample time nor authorize redispatch")
	})
	if err != nil || dispatchAuthorized || replayed != admission {
		t.Fatalf("restart replay=%+v dispatch_authorized=%v err=%v", replayed, dispatchAuthorized, err)
	}
	if _, _, err = restarted.AdmitAuxiliaryReview(ctx, frozen, reservation, "auxiliary-operation-restart-drift", time.Now); !errors.Is(err, ErrConflict) {
		t.Fatalf("new operation identity authorized duplicate review after restart: %v", err)
	}
	tx, err := restarted.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	settledCharges, unresolvedCharges, err := auxiliaryReviewCardCharges(ctx, tx, frozen.BoardID, frozen.CardID)
	_ = tx.Rollback()
	if err != nil || settledCharges != (executionCharges{}) || unresolvedCharges.timeMS != admission.TimeLimitMS ||
		unresolvedCharges.tokens != admission.TokenLimit || unresolvedCharges.costMicros != admission.CostMicros {
		t.Fatalf("restart lost conservative unresolved charge: settled=%+v unresolved=%+v err=%v", settledCharges, unresolvedCharges, err)
	}

	settledAt := admission.AdmittedAt.Add(time.Minute)
	settlement, settled, err := restarted.SettleAuxiliaryReview(ctx, admission.OperationID, workboard.AuxiliaryReviewFailed,
		AuxiliaryReviewMeasurements{}, settledAt)
	if err != nil || !settled || settlement.Validate() != nil {
		t.Fatalf("conservative settlement=%+v settled=%v err=%v", settlement, settled, err)
	}
	if settlement.ChargedTimeMS != admission.TimeLimitMS || settlement.ChargedTokens != admission.TokenLimit ||
		settlement.ChargedCostMicros != admission.CostMicros || settlement.TimeChargeMode != workboard.AuxiliaryReviewConservative ||
		settlement.TokenChargeMode != workboard.AuxiliaryReviewConservative || settlement.CostChargeMode != workboard.AuxiliaryReviewConservative {
		t.Fatalf("unknown terminal usage did not charge reservation ceilings: %+v", settlement)
	}
	if err = restarted.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	replayedSettlement, wrote, err := restarted.SettleAuxiliaryReview(ctx, admission.OperationID, workboard.AuxiliaryReviewFailed,
		AuxiliaryReviewMeasurements{}, settledAt.Add(time.Hour))
	if err != nil || wrote || replayedSettlement != settlement {
		t.Fatalf("terminal restart replay=%+v wrote=%v err=%v", replayedSettlement, wrote, err)
	}
	var admissions, settlements int
	if err = restarted.db.QueryRow(`SELECT count(*) FROM workboard_auxiliary_review_admissions WHERE operation_id=?`, admission.OperationID).Scan(&admissions); err != nil {
		t.Fatal(err)
	}
	if err = restarted.db.QueryRow(`SELECT count(*) FROM workboard_auxiliary_review_settlements WHERE operation_id=?`, admission.OperationID).Scan(&settlements); err != nil {
		t.Fatal(err)
	}
	if admissions != 1 || settlements != 1 {
		t.Fatalf("restart duplicated durable accounting: admissions=%d settlements=%d", admissions, settlements)
	}
	tx, err = restarted.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	settledCharges, unresolvedCharges, err = auxiliaryReviewCardCharges(ctx, tx, frozen.BoardID, frozen.CardID)
	_ = tx.Rollback()
	if err != nil || unresolvedCharges != (executionCharges{}) || settledCharges.timeMS != admission.TimeLimitMS ||
		settledCharges.tokens != admission.TokenLimit || settledCharges.costMicros != admission.CostMicros {
		t.Fatalf("terminal charge did not remain conservative after restart: settled=%+v unresolved=%+v err=%v", settledCharges, unresolvedCharges, err)
	}
}

func TestAuxiliaryReviewAdmissionRaceHasOneDurableWinner(t *testing.T) {
	ctx := context.Background()
	store := openExecutionAdmissionStore(t, ctx)
	defer store.Close()
	frozen, reservation, submitted, release := prepareAuxiliaryReviewCandidate(t, ctx, store, "race")
	clock := func() time.Time { return time.Date(2026, 9, 12, 15, 0, 4, 0, time.UTC) }
	type result struct {
		record  workboard.AuxiliaryReviewAdmissionRecord
		created bool
		err     error
	}
	results := make(chan result, 2)
	var start sync.WaitGroup
	start.Add(2)
	for range 2 {
		go func() {
			start.Done()
			start.Wait()
			record, created, err := store.AdmitAuxiliaryReview(ctx, frozen, reservation, "auxiliary-operation-race", clock)
			results <- result{record: record, created: created, err: err}
		}()
	}
	first, second := <-results, <-results
	if first.err != nil || second.err != nil || first.created == second.created || first.record != second.record {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	var admissions int
	if err := store.db.QueryRow(`SELECT count(*) FROM workboard_auxiliary_review_admissions WHERE board_id=? AND card_id=?`,
		frozen.BoardID, frozen.CardID).Scan(&admissions); err != nil || admissions != 1 {
		t.Fatalf("admissions=%d err=%v", admissions, err)
	}
	release()
	if err := <-submitted; err != nil {
		t.Fatal(err)
	}
}

func TestAuxiliaryReviewAdmissionSamplesClockAfterWriterWait(t *testing.T) {
	ctx := context.Background()
	store := openExecutionAdmissionStore(t, ctx)
	defer store.Close()
	frozen, reservation, submitted, release := prepareAuxiliaryReviewCandidate(t, ctx, store, "writer-clock")
	blocker, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	if err = reserveWorkboardWriter(ctx, blocker); err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 9, 12, 15, 0, 4, 0, time.UTC)
	var clockNano atomic.Int64
	clockNano.Store(clock.UnixNano())
	called := make(chan struct{}, 1)
	type result struct {
		record workboard.AuxiliaryReviewAdmissionRecord
		err    error
	}
	finished := make(chan result, 1)
	go func() {
		record, _, admitErr := store.AdmitAuxiliaryReview(ctx, frozen, reservation, "auxiliary-operation-writer-clock", func() time.Time {
			called <- struct{}{}
			return time.Unix(0, clockNano.Load()).UTC()
		})
		finished <- result{record: record, err: admitErr}
	}()
	select {
	case <-called:
		t.Fatal("clock sampled before writer ownership")
	case <-time.After(50 * time.Millisecond):
	}
	clock = clock.Add(time.Second)
	clockNano.Store(clock.UnixNano())
	if err = blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-called:
	case <-time.After(5 * time.Second):
		t.Fatal("admission did not acquire released writer")
	}
	outcome := <-finished
	if outcome.err != nil || !outcome.record.AdmittedAt.Equal(clock) {
		t.Fatalf("admission=%+v err=%v want time=%v", outcome.record, outcome.err, clock)
	}
	release()
	if err = <-submitted; err != nil {
		t.Fatal(err)
	}
}
