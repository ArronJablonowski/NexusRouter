package telemetry

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	stdRuntime "runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/workboard"
)

type auxiliaryReviewCrashEvaluator struct {
	store *Store
}

func (*auxiliaryReviewCrashEvaluator) EvaluateCandidate(context.Context, workboard.CandidateEvaluationRequest) ([]workboard.EvidenceInput, error) {
	return nil, fmt.Errorf("unbudgeted evaluator path used")
}

func (*auxiliaryReviewCrashEvaluator) AuxiliaryReviewReservation(frozen workboard.CandidateEvaluationRequest) (workboard.AuxiliaryReviewReservation, error) {
	return workboard.AuxiliaryReviewReservation{
		Version: 1, BoardID: frozen.BoardID, CardID: frozen.CardID, AttemptID: frozen.AttemptID, ClaimID: frozen.ClaimID,
		CandidateID: frozen.CandidateID, CandidateDigest: frozen.CandidateDigest, CriteriaDigest: frozen.CriteriaDigest,
		PolicyDigest: frozen.PolicyDigest, ReviewerID: "independent-reviewer", ModelID: "review-model",
		ProviderID: "review-provider", ConfigID: strings.Repeat("e", 64), TimeLimitMS: 5_000,
		TokenLimit: 1_000, CostMicros: 400,
	}, nil
}

func (e *auxiliaryReviewCrashEvaluator) EvaluateBudgetedCandidate(ctx context.Context,
	frozen workboard.CandidateEvaluationRequest,
) (workboard.BudgetedCandidateEvaluation, error) {
	operation := "candidate-review-" + frozen.CandidateID
	admission, found, err := e.store.ReplayAuxiliaryReviewAdmission(ctx, operation)
	if err != nil || !found {
		return workboard.BudgetedCandidateEvaluation{}, fmt.Errorf("durable admission missing before review result: %w", err)
	}
	timeMS, tokens, cost := int64(25), int64(12), int64(7)
	result := workboard.BudgetedCandidateEvaluation{
		Evidence: []workboard.EvidenceInput{},
		Measurements: workboard.AuxiliaryReviewMeasurements{
			TimeMS: &timeMS, Tokens: &tokens, CostMicros: &cost,
		},
		Audit: budgetedReviewAudit(frozen, admission.AdmittedAt.Add(25*time.Millisecond)),
	}
	if result.Audit.Validate() != nil || workboard.ValidateAuxiliaryReviewEvidence(frozen, result.Audit, result.Evidence) != nil {
		return workboard.BudgetedCandidateEvaluation{}, fmt.Errorf("invalid completed review fixture")
	}
	// The evaluator has produced its complete host-side result, but it has not
	// returned it to EvaluationService. A SIGKILL here therefore exercises the
	// exact admitted-review/pre-composite-commit crash window.
	fmt.Println(operation)
	select {}
}

func TestAuxiliaryReviewCrashProcessHelper(t *testing.T) {
	if os.Getenv("DARWIN_AUX_REVIEW_CRASH_HELPER") != "1" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := Open(ctx, os.Getenv("DARWIN_AUX_REVIEW_CRASH_DATABASE"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := time.Date(2026, 9, 12, 22, 0, 0, 0, time.UTC)
	evaluator := &auxiliaryReviewCrashEvaluator{store: store}
	service, request := prepareBudgetedReviewService(t, store, evaluator, &clock, "kill")
	if _, err = service.SubmitCandidate(ctx, request); err != nil {
		t.Fatal(err)
	}
	t.Fatal("review helper returned past crash boundary")
}

func TestAuxiliaryReviewAdmissionRecoversAfterSIGKILLBeforeCompositeCommit(t *testing.T) {
	if stdRuntime.GOOS != "darwin" && stdRuntime.GOOS != "linux" {
		t.Skip("SIGKILL qualification requires Unix")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	database := t.TempDir() + "/review-crash.db"
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAuxiliaryReviewCrashProcessHelper$", "-test.count=1")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "DARWIN_AUX_REVIEW_CRASH_HELPER=1", "DARWIN_AUX_REVIEW_CRASH_DATABASE=" + database}
	cmd.Stderr, cmd.WaitDelay = io.Discard, time.Second
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, scanned := make(chan string, 1), make(chan struct{})
	go func() {
		defer close(scanned)
		scanner := bufio.NewScanner(pipe)
		scanner.Buffer(make([]byte, 256), 4096)
		if scanner.Scan() {
			line <- scanner.Text()
		} else {
			line <- ""
		}
	}()
	waited := false
	defer func() {
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		<-scanned
	}()
	var operation string
	select {
	case operation = <-line:
	case <-ctx.Done():
		t.Fatal("review helper did not reach durable crash boundary")
	}
	if operation == "" || len(operation) > 128 || strings.ContainsAny(operation, " \t\r\n") {
		t.Fatal("invalid crash-boundary acknowledgement")
	}
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	waited = true
	if err == nil || cmd.ProcessState == nil {
		t.Fatal("review helper returned instead of being killed")
	}
	if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatal("review helper did not die by SIGKILL")
	}

	store, err := Open(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	admission, found, err := store.ReplayAuxiliaryReviewAdmission(ctx, operation)
	if err != nil || !found || admission.Validate() != nil {
		t.Fatalf("admission=%+v found=%v err=%v", admission, found, err)
	}
	var candidates, settlements, audits, outcomes int
	if err = store.db.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM workboard_candidates WHERE board_id=? AND card_id=? AND attempt_id=?),
		(SELECT count(*) FROM workboard_auxiliary_review_settlements WHERE admission_id=?),
		(SELECT count(*) FROM audit_records WHERE task_id IN (SELECT task_id FROM workboard_execution_admissions WHERE board_id=? AND card_id=?)),
		(SELECT count(*) FROM workboard_auxiliary_review_outcomes WHERE admission_id=?)`, admission.BoardID, admission.CardID,
		admission.AttemptID, admission.AdmissionID, admission.BoardID, admission.CardID, admission.AdmissionID).Scan(
		&candidates, &settlements, &audits, &outcomes); err != nil || candidates != 0 || settlements != 0 || audits != 0 || outcomes != 0 {
		t.Fatalf("partial composite survived: candidate=%d settlement=%d audit=%d outcome=%d err=%v", candidates, settlements, audits, outcomes, err)
	}
	deadline := admission.AdmittedAt.Add(time.Duration(admission.TimeLimitMS) * time.Millisecond)
	if recovered, err := store.RecoverExpiredAuxiliaryReview(ctx, operation, func() time.Time { return deadline }); err != nil || !recovered {
		t.Fatalf("recovered=%v err=%v", recovered, err)
	}
	settlement, found, err := store.ReplayAuxiliaryReviewSettlement(ctx, operation, workboard.AuxiliaryReviewFailed)
	if err != nil || !found || settlement.ChargedTimeMS != admission.TimeLimitMS ||
		settlement.ChargedTokens != admission.TokenLimit || settlement.ChargedCostMicros != admission.CostMicros ||
		settlement.TimeChargeMode != workboard.AuxiliaryReviewConservative ||
		settlement.TokenChargeMode != workboard.AuxiliaryReviewConservative ||
		settlement.CostChargeMode != workboard.AuxiliaryReviewConservative {
		t.Fatalf("settlement=%+v found=%v err=%v", settlement, found, err)
	}
	if recovered, err := store.RecoverExpiredAuxiliaryReview(ctx, operation, func() time.Time {
		panic("terminal recovery must not resample time")
	}); err != nil || recovered {
		t.Fatalf("terminal replay recovered=%v err=%v", recovered, err)
	}
}

type auxiliaryReviewAfterCommitStore struct {
	*Store
}

func (s *auxiliaryReviewAfterCommitStore) ApplyEvaluationMutationAndSettleAuxiliaryReview(ctx context.Context,
	frozen workboard.CandidateEvaluationRequest, mutation workboard.EvaluationMutation, audit evaluation.AuditRecord,
	now func() time.Time, operation string, measurements workboard.AuxiliaryReviewMeasurements,
) (workboard.OperationReceipt, workboard.AuxiliaryReviewSettlementRecord, workboard.AuxiliaryReviewOutcomeRecord, error) {
	receipt, settlement, outcome, err := s.Store.ApplyEvaluationMutationAndSettleAuxiliaryReview(
		ctx, frozen, mutation, audit, now, operation, measurements)
	if err == nil {
		// The SQLite commit is complete, but EvaluationService has not received
		// the return values and cannot acknowledge SubmitCandidate.
		fmt.Println(operation)
		select {}
	}
	return receipt, settlement, outcome, err
}

func TestAuxiliaryReviewAfterCommitCrashProcessHelper(t *testing.T) {
	if os.Getenv("DARWIN_AUX_REVIEW_AFTER_COMMIT_HELPER") != "1" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := Open(ctx, os.Getenv("DARWIN_AUX_REVIEW_CRASH_DATABASE"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := time.Date(2026, 9, 12, 23, 0, 0, 0, time.UTC)
	evaluator := &budgetedReviewFixture{}
	_, request := prepareBudgetedReviewService(t, store, evaluator, &clock, "ack")
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal("persist crash fixture request", err)
	}
	if err = os.WriteFile(os.Getenv("DARWIN_AUX_REVIEW_REQUEST"), body, 0600); err != nil {
		t.Fatal("persist crash fixture request", err)
	}
	service, err := workboard.NewEvaluationService(&auxiliaryReviewAfterCommitStore{Store: store},
		telemetryCardAuthority{authority: workboard.Authority{CreationScope: "acceptance-authority", Actor: workboard.Actor{ID: "integrated-worker-ack", Type: "worker"}}},
		evaluator, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.SubmitCandidate(ctx, request); err != nil {
		t.Fatal(err)
	}
	t.Fatal("review helper acknowledged committed review")
}

func TestAuxiliaryReviewOutcomeReplaysAfterSIGKILLBeforeAcknowledgement(t *testing.T) {
	if stdRuntime.GOOS != "darwin" && stdRuntime.GOOS != "linux" {
		t.Skip("SIGKILL qualification requires Unix")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	root := t.TempDir()
	database, requestPath := root+"/review-ack-crash.db", root+"/request.json"
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAuxiliaryReviewAfterCommitCrashProcessHelper$", "-test.count=1")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "DARWIN_AUX_REVIEW_AFTER_COMMIT_HELPER=1",
		"DARWIN_AUX_REVIEW_CRASH_DATABASE=" + database, "DARWIN_AUX_REVIEW_REQUEST=" + requestPath}
	cmd.Stderr, cmd.WaitDelay = io.Discard, time.Second
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, scanned := make(chan string, 1), make(chan struct{})
	go func() {
		defer close(scanned)
		scanner := bufio.NewScanner(pipe)
		scanner.Buffer(make([]byte, 256), 4096)
		if scanner.Scan() {
			line <- scanner.Text()
		} else {
			line <- ""
		}
	}()
	waited := false
	defer func() {
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		<-scanned
	}()
	var operation string
	select {
	case operation = <-line:
	case <-ctx.Done():
		t.Fatal("review helper missed post-commit crash boundary")
	}
	if operation == "" || len(operation) > 128 || strings.ContainsAny(operation, " \t\r\n") {
		t.Fatal("invalid post-commit acknowledgement")
	}
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	waited = true
	if err == nil || cmd.ProcessState == nil {
		t.Fatal("post-commit helper returned instead of being killed")
	}
	if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatal("post-commit helper did not die by SIGKILL")
	}

	body, err := os.ReadFile(requestPath)
	if err != nil {
		t.Fatal(err)
	}
	var request workboard.SubmitCandidateRequest
	if json.Unmarshal(body, &request) != nil {
		t.Fatal("invalid persisted crash fixture request")
	}
	store, err := Open(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)
	evaluator := &budgetedReviewFixture{}
	service := newTestEvaluationService(t, store, workboard.Actor{ID: "integrated-worker-ack", Type: "worker"}, evaluator, &clock)
	receipt, err := service.SubmitCandidate(ctx, request)
	if err != nil || receipt.Validate() != nil || evaluator.calls.Load() != 0 {
		t.Fatalf("restart replay=%+v evaluator_calls=%d err=%v", receipt, evaluator.calls.Load(), err)
	}
	outcome, found, legacy, err := store.ReplayAuxiliaryReviewOutcome(ctx, operation)
	if err != nil || !found || legacy || outcome.Validate() != nil {
		t.Fatalf("outcome=%+v found=%v legacy=%v err=%v", outcome, found, legacy, err)
	}
	settlement, found, err := store.ReplayAuxiliaryReviewSettlement(ctx, operation, workboard.AuxiliaryReviewCompleted)
	if err != nil || !found || settlement.SettlementDigest != outcome.SettlementDigest {
		t.Fatalf("settlement=%+v found=%v err=%v", settlement, found, err)
	}
	var admissions, settlements, outcomes, candidates int
	if err = store.db.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM workboard_auxiliary_review_admissions WHERE operation_id=?),
		(SELECT count(*) FROM workboard_auxiliary_review_settlements WHERE operation_id=?),
		(SELECT count(*) FROM workboard_auxiliary_review_outcomes WHERE operation_id=?),
		(SELECT count(*) FROM workboard_candidates WHERE id=?)`, operation, operation, operation, outcome.CandidateID).Scan(
		&admissions, &settlements, &outcomes, &candidates); err != nil ||
		admissions != 1 || settlements != 1 || outcomes != 1 || candidates != 1 {
		t.Fatalf("duplicate or missing replay state: admission=%d settlement=%d outcome=%d candidate=%d err=%v",
			admissions, settlements, outcomes, candidates, err)
	}
}
