package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func completeReviewFixture(t *testing.T, s *Store) (evaluation.ReviewAttempt, evaluation.AuditRecord) {
	t.Helper()
	auditEvents(t, s, runtime.TaskStarted, runtime.TurnStarted, runtime.TurnCompleted, runtime.TaskCompleted)
	r := reviewStart()
	if err := s.BeginReview(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	a := storedAudit()
	r.Status, r.AuditID, r.FinishedAt = "completed", a.ID, r.StartedAt.Add(time.Second)
	return r, a
}

func TestCompleteReviewAtomicRetryAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "review.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	r, a := completeReviewFixture(t, s)
	if err := s.CompleteReview(ctx, r, a); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteReview(ctx, r, a); err != nil {
		t.Fatal("exact retry", err)
	}
	changed := a
	changed.Audit.Confidence = .5
	if err := s.CompleteReview(ctx, r, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("changed audit accepted", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.ReviewAttempt(ctx, r.ID)
	if err != nil || got.Status != "completed" || got.AuditID != a.ID {
		t.Fatal(got, err)
	}
	audits, err := s.Audits(ctx, r.TaskID, "", 100)
	if err != nil || len(audits) != 1 || audits[0].Audit.Confidence != a.Audit.Confidence {
		t.Fatal(audits, err)
	}
}

func TestCompleteReviewFailureAndRollbackNoOrphan(t *testing.T) {
	for _, mode := range []string{"terminal", "trigger"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			s, err := Open(ctx, filepath.Join(t.TempDir(), "review.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			r, a := completeReviewFixture(t, s)
			want := "started"
			if mode == "terminal" {
				failed := r
				failed.Status, failed.AuditID, failed.Code = "failed", "", "canceled"
				if err := s.FinishReview(ctx, failed); err != nil {
					t.Fatal(err)
				}
				want = "failed"
			} else {
				if _, err := s.db.Exec(`CREATE TRIGGER fail_complete BEFORE UPDATE ON review_attempts BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.CompleteReview(ctx, r, a); err == nil {
				t.Fatal("completion unexpectedly succeeded")
			}
			if _, err := s.Audit(ctx, a.ID); !errors.Is(err, sql.ErrNoRows) {
				t.Fatal("orphan audit", err)
			}
			got, err := s.ReviewAttempt(ctx, r.ID)
			if err != nil || got.Status != want {
				t.Fatal(got, err)
			}
		})
	}
}

func TestCompleteReviewRejectsInvalidLinkage(t *testing.T) {
	for _, mutate := range []func(*evaluation.ReviewAttempt, *evaluation.AuditRecord){
		func(r *evaluation.ReviewAttempt, a *evaluation.AuditRecord) { r.AuditID = "different" },
		func(r *evaluation.ReviewAttempt, a *evaluation.AuditRecord) { a.TaskID = "other" },
		func(r *evaluation.ReviewAttempt, a *evaluation.AuditRecord) { a.AttemptID = "other" },
		func(r *evaluation.ReviewAttempt, a *evaluation.AuditRecord) { a.EvaluatorModel = "other" },
		func(r *evaluation.ReviewAttempt, a *evaluation.AuditRecord) { a.EvaluatorProvider = "other" },
		func(r *evaluation.ReviewAttempt, a *evaluation.AuditRecord) { a.Version = 0 },
		func(r *evaluation.ReviewAttempt, a *evaluation.AuditRecord) {
			r.StartedAt = r.StartedAt.Add(-time.Second)
		},
	} {
		ctx := context.Background()
		s, err := Open(ctx, filepath.Join(t.TempDir(), "review.db"))
		if err != nil {
			t.Fatal(err)
		}
		r, a := completeReviewFixture(t, s)
		mutate(&r, &a)
		if err := s.CompleteReview(ctx, r, a); err == nil {
			t.Fatal("invalid linkage accepted")
		}
		var count int
		if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM audit_records").Scan(&count); err != nil || count != 0 {
			t.Fatal("partial write", count, err)
		}
		got, err := s.ReviewAttempt(ctx, r.ID)
		if err != nil || got.Status != "started" {
			t.Fatal(got, err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCompleteReviewConcurrentFailure(t *testing.T) {
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
	r, a := completeReviewFixture(t, s)
	failed := r
	failed.Status, failed.AuditID, failed.Code = "failed", "", "canceled"
	var wg sync.WaitGroup
	results := make(chan error, 2)
	start := make(chan struct{})
	for _, op := range []func() error{func() error { return s.CompleteReview(ctx, r, a) }, func() error { return other.FinishReview(ctx, failed) }} {
		wg.Add(1)
		go func(op func() error) { defer wg.Done(); <-start; results <- op() }(op)
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
	got, err := s.ReviewAttempt(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, auditErr := s.Audit(ctx, a.ID)
	if (got.Status == "completed" && auditErr != nil) || (got.Status == "failed" && !errors.Is(auditErr, sql.ErrNoRows)) || (got.Status != "completed" && got.Status != "failed") {
		t.Fatal("inconsistent terminal state", got.Status, auditErr)
	}
}
