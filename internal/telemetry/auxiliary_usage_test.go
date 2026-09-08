package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/accounting"
	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func TestAuditEvidenceHasOneReviewOwnerUnderRace(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "exclusive-audit.db")
	first, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	auditEvents(t, first, runtime.TaskStarted, runtime.TurnStarted, runtime.TurnCompleted, runtime.TaskCompleted)
	second, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	one, two := publicReviewStart(), publicReviewStart()
	one.ID, two.ID = "review-one", "review-two"
	one.EstimatedCost, two.EstimatedCost = .1, .1
	if err := first.BeginReview(ctx, one); err != nil {
		t.Fatal(err)
	}
	if err := second.BeginReview(ctx, two); err != nil {
		t.Fatal(err)
	}
	audit := storedAudit()
	one.Status, one.AuditID, one.FinishedAt = "completed", audit.ID, one.StartedAt.Add(time.Second)
	two.Status, two.AuditID, two.FinishedAt = "completed", audit.ID, two.StartedAt.Add(time.Second)
	type outcome struct {
		id  string
		err error
	}
	start := make(chan struct{})
	results := make(chan outcome, 2)
	var wg sync.WaitGroup
	for _, work := range []struct {
		store   *Store
		attempt evaluation.ReviewAttempt
	}{{first, one}, {second, two}} {
		wg.Add(1)
		go func(store *Store, attempt evaluation.ReviewAttempt) {
			defer wg.Done()
			<-start
			results <- outcome{attempt.ID, store.CompleteReview(ctx, attempt, audit)}
		}(work.store, work.attempt)
	}
	close(start)
	wg.Wait()
	close(results)
	winner := ""
	for result := range results {
		if result.err == nil {
			if winner != "" {
				t.Fatal("audit evidence acquired two owners", winner, result.id)
			}
			winner = result.id
		} else if !errors.Is(result.err, ErrConflict) {
			t.Fatal("unexpected competing completion error", result.err)
		}
	}
	if winner == "" {
		t.Fatal("no completion won")
	}
	winnerAttempt := one
	if winner == two.ID {
		winnerAttempt = two
	}
	if err := first.CompleteReview(ctx, winnerAttempt, audit); err != nil {
		t.Fatal("exact winner replay", err)
	}
	var reviews, usage int
	if err := first.db.QueryRowContext(ctx, "SELECT count(*) FROM review_attempts WHERE status='completed' AND json_extract(body,'$.AuditID')=?", audit.ID).Scan(&reviews); err != nil {
		t.Fatal(err)
	}
	if err := first.db.QueryRowContext(ctx, "SELECT count(*) FROM usage_records WHERE evidence_id=?", audit.ID).Scan(&usage); err != nil || reviews != 1 || usage != 1 {
		t.Fatal("exclusive linkage not preserved", reviews, usage, err)
	}

	// Corruption must fail closed during evidence validation instead of letting
	// SQLite select one arbitrary completed review.
	loser := two
	if winner == two.ID {
		loser = one
	}
	body, err := json.Marshal(loser)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.db.ExecContext(ctx, "UPDATE review_attempts SET status='completed',body=? WHERE id=?", body, loser.ID); err != nil {
		t.Fatal(err)
	}
	record, err := first.CurrentUsage(ctx, usageID(accounting.OrchestratorAudit, winner))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := first.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := validateUsageEvidence(ctx, tx, record, true); !errors.Is(err, accounting.ErrUsage) {
		t.Fatal("duplicate audit linkage did not fail closed", err)
	}
}

func TestReviewUsageAccountingRolesReplayAndFailure(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "review-usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	auditEvents(t, s, runtime.TaskStarted, runtime.TurnStarted, runtime.TurnCompleted, runtime.TaskCompleted)

	public := publicReviewStart()
	public.EstimatedCost = .25
	if err := s.BeginReview(ctx, public); err != nil {
		t.Fatal(err)
	}
	audit := storedAudit()
	public.Status, public.AuditID, public.FinishedAt = "completed", audit.ID, public.StartedAt.Add(time.Second)
	if err := s.CompleteReview(ctx, public, audit); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteReview(ctx, public, audit); err != nil {
		t.Fatal("lost-ack replay:", err)
	}
	r, err := s.CurrentUsage(ctx, usageID(accounting.OrchestratorAudit, public.ID))
	if err != nil || r.Role != accounting.OrchestratorAudit || r.OperationID != public.ID || r.RouteID != public.ID || r.EvidenceID != audit.ID || r.AuditID != audit.ID || r.CandidateAttemptID != public.AttemptID || r.Usage == nil || *r.Usage != *audit.Usage || r.NormalizedCost == nil || *r.NormalizedCost != .25 || r.Pricing == nil || r.Pricing.Basis != accounting.ConfiguredEstimate {
		t.Fatalf("public audit usage: %#v %v", r, err)
	}

	legacy := reviewStart()
	legacy.ID, legacy.EstimatedCost = "review-legacy", .5
	if err := s.BeginReview(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	legacy.Status, legacy.Code, legacy.FinishedAt = "failed", "review_failed", legacy.StartedAt.Add(2*time.Second)
	if err := s.FinishReview(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	r, err = s.CurrentUsage(ctx, usageID(accounting.OptionalJudge, legacy.ID))
	if err != nil || r.Role != accounting.OptionalJudge || r.EvidenceKind != accounting.ReviewEvidence || r.Disposition != accounting.Failed || r.RetryClass != accounting.NonRetryable || r.Usage != nil || r.NormalizedCost == nil || *r.NormalizedCost != .5 {
		t.Fatalf("legacy judge usage: %#v %v", r, err)
	}

	totals, err := s.UsageTotals(ctx, accounting.Scope{TaskID: public.TaskID})
	if err != nil || totals.OrchestratorAudit.Records != 1 || totals.Judge.Records != 1 || totals.Auxiliary.Records != 2 || totals.Routed.Records != 1 || totals.Overall.Records != 3 {
		t.Fatalf("separated totals: %#v %v", totals, err)
	}
	var fitness int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM fitness").Scan(&fitness); err != nil || fitness != 0 {
		t.Fatal("audit accounting affected fitness", fitness, err)
	}
}

func TestReviewUsageAtomicRollbackAndCancellation(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "review-atomic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	auditEvents(t, s, runtime.TaskStarted, runtime.TurnStarted, runtime.TurnCompleted, runtime.TaskCompleted)
	r := publicReviewStart()
	r.EstimatedCost = .1
	if err := s.BeginReview(ctx, r); err != nil {
		t.Fatal(err)
	}
	a := storedAudit()
	r.Status, r.AuditID, r.FinishedAt = "completed", a.ID, r.StartedAt.Add(time.Second)
	if _, err := s.db.Exec(`CREATE TRIGGER reject_review_usage BEFORE INSERT ON usage_records WHEN NEW.role='orchestrator_audit' BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteReview(ctx, r, a); err == nil {
		t.Fatal("usage persistence failure ignored")
	}
	if _, err := s.Audit(ctx, a.ID); err == nil {
		t.Fatal("audit evidence survived rolled-back usage write")
	}
	pending, err := s.ReviewAttempt(ctx, r.ID)
	if err != nil || pending.Status != "started" {
		t.Fatal("review terminal survived rolled-back usage write", pending, err)
	}
	if _, err := s.db.Exec("DROP TRIGGER reject_review_usage"); err != nil {
		t.Fatal(err)
	}
	canceled, err := s.CancelReview(ctx, r.TaskID, r.ID, r.StartedAt.Add(2*time.Second))
	if err != nil || canceled.Code != "canceled" {
		t.Fatal(canceled, err)
	}
	usage, err := s.CurrentUsage(ctx, usageID(accounting.OrchestratorAudit, r.ID))
	if err != nil || usage.Disposition != accounting.Canceled || usage.Usage != nil {
		t.Fatal(usage, err)
	}
	again, err := s.CancelReview(ctx, r.TaskID, r.ID, r.StartedAt.Add(3*time.Second))
	if err != nil || again != canceled {
		t.Fatal("cancel replay", again, err)
	}
}

func TestSummaryUsageAccountingAndAtomicRollback(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "summary-usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a, draft := summaryAttemptFixture(t, s)
	if err := s.BeginSummary(ctx, a); err != nil {
		t.Fatal(err)
	}
	done := a
	done.Status, done.Draft, done.FinishedAt = "drafted", draft, a.StartedAt.Add(time.Second)
	if err := s.CompleteSummary(ctx, done); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteSummary(ctx, done); err != nil {
		t.Fatal("summary replay:", err)
	}
	r, err := s.CurrentUsage(ctx, usageID(accounting.Summarizer, a.ID))
	if err != nil || r.Role != accounting.Summarizer || r.OperationID != a.ID || r.RouteID != a.ID || r.EvidenceID != a.ID || r.Usage == nil || *r.Usage != *draft.Usage || r.NormalizedCost == nil || *r.NormalizedCost != a.EstimatedCost || r.Pricing == nil || r.Pricing.Basis != accounting.ConfiguredEstimate {
		t.Fatalf("summary usage: %#v %v", r, err)
	}

	second := a
	second.ID, second.StartedAt = "summary-failure", a.StartedAt.Add(5*time.Second)
	if err := s.BeginSummary(ctx, second); err != nil {
		t.Fatal(err)
	}
	failed := second
	failed.Status, failed.Code, failed.FinishedAt = "failed", "summary_failed", second.StartedAt.Add(time.Second)
	if _, err := s.db.Exec(`CREATE TRIGGER reject_summary_usage BEFORE INSERT ON usage_records WHEN NEW.role='summarizer' AND NEW.operation_id='summary-failure' BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishSummary(ctx, failed); err == nil {
		t.Fatal("summary usage persistence failure ignored")
	}
	pending, err := s.SummaryAttempt(ctx, second.ID)
	if err != nil || pending.Status != "started" {
		t.Fatal("summary terminal survived rolled-back usage write", pending, err)
	}
	if _, err := s.CurrentUsage(ctx, usageID(accounting.Summarizer, second.ID)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("usage unexpectedly persisted", err)
	}
}

func TestReviewAttemptRejectsInvalidEstimatedCost(t *testing.T) {
	r := reviewStart()
	r.EstimatedCost = -1
	if !errors.Is(r.Validate(), evaluation.ErrAudit) {
		t.Fatal("negative estimate accepted")
	}
}
