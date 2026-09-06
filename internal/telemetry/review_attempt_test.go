package telemetry

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func reviewStart() evaluation.ReviewAttempt {
	return evaluation.ReviewAttempt{Version: 1, ID: "review-a", TaskID: "task", AttemptID: "attempt", EvaluatorModel: "independent-reviewer", EvaluatorProvider: "review-provider", Status: "started", StartedAt: time.Unix(100, 0).UTC()}
}

func TestReviewLifecycleRestartAndCAS(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "review.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	r := reviewStart()
	if err := s.BeginReview(ctx, r); !errors.Is(err, evaluation.ErrAudit) {
		t.Fatal("absent task accepted", err)
	}
	auditEvents(t, s, runtime.TaskStarted, runtime.TurnStarted, runtime.TurnCompleted, runtime.TaskFailed)
	if err := s.BeginReview(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginReview(ctx, r); err != nil {
		t.Fatal("retry", err)
	}
	changed := r
	changed.EvaluatorModel = "other"
	if err := s.BeginReview(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("mutated start", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.ReviewAttempt(ctx, r.ID)
	if err != nil || got.Status != "started" {
		t.Fatal(got, err)
	}
	done := r
	done.Status, done.AuditID, done.FinishedAt = "completed", "audit-a", r.StartedAt.Add(time.Second)
	if err := s.FinishReview(ctx, done); err == nil {
		t.Fatal("missing audit accepted")
	}
	a := storedAudit()
	a.EvaluatorModel = "other"
	if err := s.RecordAudit(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishReview(ctx, done); !errors.Is(err, evaluation.ErrAudit) {
		t.Fatal("wrong evaluator accepted", err)
	}
	a.ID, a.EvaluatorModel = "audit-correct", r.EvaluatorModel
	if err := s.RecordAudit(ctx, a); err != nil {
		t.Fatal(err)
	}
	done.AuditID = a.ID
	if err := s.FinishReview(ctx, done); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishReview(ctx, done); err != nil {
		t.Fatal("terminal retry", err)
	}
	failed := r
	failed.Status, failed.Code, failed.FinishedAt = "failed", "canceled", done.FinishedAt
	if err := s.FinishReview(ctx, failed); !errors.Is(err, ErrConflict) {
		t.Fatal("terminal overwrite", err)
	}
	r.ID = "review-b"
	if err := s.BeginReview(ctx, r); err != nil {
		t.Fatal(err)
	}
	failed.ID = r.ID
	if err := s.FinishReview(ctx, failed); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	page, err := s.ReviewAttempts(ctx, "task", "", 1)
	if err != nil || len(page) != 1 || page[0].Status != "completed" {
		t.Fatal(page, err)
	}
	page, err = s.ReviewAttempts(ctx, "task", "review-a", 100)
	if err != nil || len(page) != 1 || page[0].Code != "canceled" {
		t.Fatal(page, err)
	}
	r.ID = "review-c"
	if err := s.BeginReview(ctx, r); err == nil {
		t.Fatal("readonly write accepted")
	}
	if _, err := s.ReviewAttempts(ctx, "task", "", 101); err == nil {
		t.Fatal("unbounded page")
	}
}

func TestReviewMigrationAndCorruptRead(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "review.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("DROP INDEX events_submission_start; DROP TABLE lease_attention; DROP TABLE lease_recoveries; ALTER TABLE resource_leases DROP COLUMN process_id; DROP TABLE lease_processes; DROP TABLE learning_states; DROP TABLE memory_retired_ids; DROP TABLE workflow_scan_buckets; DROP TABLE workflow_scan_consumptions; DROP TABLE workflow_scan_consumers; DROP TRIGGER workflow_scan_task_insert; DROP TABLE workflow_scan_tasks; DROP TABLE workflow_scan_pages; DROP TABLE workflow_scans; DROP TABLE workflow_selections; DROP TABLE skill_generation_attempts; DROP TABLE tool_approvals; DROP TABLE task_steering; DROP TABLE submission_recoveries; DROP TABLE submissions; DROP TABLE task_cancellations; DROP TABLE summary_review_heads; DROP TABLE summary_reviews; DROP TABLE summary_attempts; DROP INDEX events_model_start; DROP TABLE review_attempts; PRAGMA user_version=6;"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	auditEvents(t, s, runtime.TaskStarted, runtime.TurnStarted, runtime.TurnCompleted, runtime.TaskCompleted)
	r := reviewStart()
	if err := s.BeginReview(ctx, r); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE review_attempts SET status='completed'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReviewAttempt(ctx, r.ID); !errors.Is(err, evaluation.ErrAudit) {
		t.Fatal("corrupt read", err)
	}
	if _, err := s.ReviewAttempts(ctx, "task", "", 100); !errors.Is(err, evaluation.ErrAudit) {
		t.Fatal("corrupt list", err)
	}
}

func TestReviewConcurrentFinish(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "review.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	other, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	auditEvents(t, s, runtime.TaskStarted, runtime.TurnStarted, runtime.TurnCompleted, runtime.TaskCompleted)
	r := reviewStart()
	if err := s.BeginReview(ctx, r); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i, store := range []*Store{s, other} {
		wg.Add(1)
		go func(i int, store *Store) {
			defer wg.Done()
			terminal := r
			terminal.Status, terminal.FinishedAt = "failed", r.StartedAt.Add(time.Second)
			terminal.Code = []string{"canceled", "review_failed"}[i]
			results <- store.FinishReview(ctx, terminal)
		}(i, store)
	}
	wg.Wait()
	close(results)
	wins, conflicts := 0, 0
	for err := range results {
		if err == nil {
			wins++
		} else if errors.Is(err, ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatal(wins, conflicts)
	}
}
