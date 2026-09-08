package telemetry

import (
	"context"
	"database/sql"
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

func publicReviewStart() evaluation.ReviewAttempt {
	r := reviewStart()
	r.ReviewerID = "reviewer"
	r.RequestDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	return r
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
	if _, err := s.db.Exec("DROP INDEX events_submission_start; DROP TABLE task_timings; DROP TABLE task_timing_metadata; DROP INDEX events_task_kind; DROP TABLE skill_exposures; DROP INDEX task_heads_session; DROP TABLE learning_activation_intents; DROP TABLE lease_attention_history; DROP TABLE lease_attention; DROP TABLE lease_recoveries; ALTER TABLE resource_leases DROP COLUMN process_id; DROP TABLE lease_processes; DROP TABLE learning_states; DROP TABLE memory_retired_ids; DROP TABLE workflow_scan_buckets; DROP TABLE workflow_scan_consumptions; DROP TABLE workflow_scan_consumers; DROP TRIGGER workflow_scan_task_insert; DROP TABLE workflow_scan_tasks; DROP TABLE workflow_scan_pages; DROP TABLE workflow_scans; DROP TABLE workflow_selections; DROP TABLE skill_generation_attempts; DROP TABLE tool_approvals; DROP TABLE task_steering; DROP TABLE submission_recoveries; DROP TABLE submissions; DROP TABLE task_cancellations; DROP TABLE summary_review_heads; DROP TABLE summary_reviews; DROP TABLE summary_attempts; DROP INDEX events_model_start; DROP TABLE review_attempts; DROP TABLE IF EXISTS usage_corrections; DROP TABLE IF EXISTS usage_heads; DROP TABLE IF EXISTS usage_records; DROP TABLE IF EXISTS usage_metadata; DROP INDEX IF EXISTS evaluations_routing_key; PRAGMA user_version=6;"); err != nil {
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

func TestAdmitReviewLostAckAndIdentityConflict(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "review.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	auditEvents(t, s, runtime.TaskStarted, runtime.TurnStarted, runtime.TurnCompleted, runtime.TaskCompleted)
	r := publicReviewStart()
	got, created, err := s.AdmitReview(ctx, r)
	if err != nil || !created || got != r {
		t.Fatal(got, created, err)
	}
	// Simulate a lost acknowledgement: the retry has no access to the original
	// admission time, but its immutable request identity is exact.
	retry := r
	retry.StartedAt = retry.StartedAt.Add(time.Hour)
	got, created, err = s.AdmitReview(ctx, retry)
	if err != nil || created || !got.StartedAt.Equal(r.StartedAt) {
		t.Fatal(got, created, err)
	}
	for _, mutate := range []func(*evaluation.ReviewAttempt){
		func(v *evaluation.ReviewAttempt) {
			v.RequestDigest = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		},
		func(v *evaluation.ReviewAttempt) { v.ReviewerID = "other" },
		func(v *evaluation.ReviewAttempt) { v.AttemptID = "other" },
	} {
		changed := retry
		mutate(&changed)
		if _, _, err := s.AdmitReview(ctx, changed); !errors.Is(err, ErrConflict) {
			t.Fatal("identity mismatch accepted", err)
		}
	}
	legacy := reviewStart()
	legacy.ID = "legacy-public"
	if _, _, err := s.AdmitReview(ctx, legacy); !errors.Is(err, evaluation.ErrAudit) {
		t.Fatal("legacy row admitted as public operation", err)
	}
}

func TestAdmitReviewConcurrentOneCreated(t *testing.T) {
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
	base := publicReviewStart()
	start := make(chan struct{})
	type result struct {
		r       evaluation.ReviewAttempt
		created bool
		err     error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for i, store := range []*Store{s, other} {
		wg.Add(1)
		go func(i int, store *Store) {
			defer wg.Done()
			<-start
			r := base
			r.StartedAt = r.StartedAt.Add(time.Duration(i) * time.Second)
			got, created, err := store.AdmitReview(ctx, r)
			results <- result{got, created, err}
		}(i, store)
	}
	close(start)
	wg.Wait()
	close(results)
	created := 0
	var admitted time.Time
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.created {
			created++
		}
		if admitted.IsZero() {
			admitted = result.r.StartedAt
		} else if !admitted.Equal(result.r.StartedAt) {
			t.Fatal("replay did not return admitted row", admitted, result.r.StartedAt)
		}
	}
	if created != 1 {
		t.Fatal("created count", created)
	}
}

func TestAdmitReviewConcurrentIdentityConflict(t *testing.T) {
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
	requests := []evaluation.ReviewAttempt{publicReviewStart(), publicReviewStart()}
	requests[1].RequestDigest = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i, store := range []*Store{s, other} {
		wg.Add(1)
		go func(i int, store *Store) {
			defer wg.Done()
			<-start
			_, _, err := store.AdmitReview(ctx, requests[i])
			results <- err
		}(i, store)
	}
	close(start)
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

func TestCancelReviewTerminalAndCompletionRace(t *testing.T) {
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
	r := publicReviewStart()
	if _, _, err := s.AdmitReview(ctx, r); err != nil {
		t.Fatal(err)
	}
	a := storedAudit()
	a.Audit.EvaluatorID = r.ReviewerID
	done := r
	done.Status, done.AuditID, done.FinishedAt = "completed", a.ID, r.StartedAt.Add(2*time.Second)
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); <-start; results <- s.CompleteReview(ctx, done, a) }()
	go func() {
		defer wg.Done()
		<-start
		_, err := other.CancelReview(ctx, r.TaskID, r.ID, r.StartedAt.Add(time.Second))
		results <- err
	}()
	close(start)
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
	terminal, err := s.ReviewAttempt(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.CancelReview(ctx, r.TaskID, r.ID, r.StartedAt.Add(3*time.Second))
	if err != nil || again.Status != terminal.Status || !again.FinishedAt.Equal(terminal.FinishedAt) {
		t.Fatal(again, terminal, err)
	}
	if terminal.Status == "failed" {
		if wins != 1 || conflicts != 1 {
			t.Fatal("cancellation-first result", wins, conflicts)
		}
		if _, err := s.Audit(ctx, a.ID); !errors.Is(err, sql.ErrNoRows) {
			t.Fatal("cancellation left audit evidence", err)
		}
	} else if terminal.Status == "completed" {
		if wins != 2 || conflicts != 0 {
			t.Fatal("completion-first result", wins, conflicts)
		}
	} else {
		t.Fatal("unexpected terminal state", terminal.Status)
	}

	// Prove the stronger ordering guarantee without scheduler dependence.
	r.ID, a.ID = "review-cancel-first", "audit-cancel-first"
	done.ID, done.AuditID = r.ID, a.ID
	if _, _, err := s.AdmitReview(ctx, r); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelReview(ctx, r.TaskID, r.ID, r.StartedAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteReview(ctx, done, a); !errors.Is(err, ErrConflict) {
		t.Fatal("completion replaced cancellation", err)
	}
	if _, err := s.Audit(ctx, a.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("cancellation-first left audit evidence", err)
	}
}
