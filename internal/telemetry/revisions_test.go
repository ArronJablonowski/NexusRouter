package telemetry

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"darwinrouter/evaluation"
	"darwinrouter/routing"
	"darwinrouter/runtime"
)

func revisionBase(t *testing.T, s *Store) evaluation.Record {
	t.Helper()
	auditEvents(t, s, runtime.TaskStarted, runtime.TurnStarted, runtime.TurnCompleted, runtime.TaskCompleted)
	r := evaluation.Record{Version: 1, ID: "base", TaskID: "task", AttemptID: "attempt", Key: routing.Key{Model: "candidate", Provider: "candidate-provider", Domain: "creative", Profile: "default"}, Checks: []evaluation.Check{{Source: evaluation.LLMJudge, Reference: "judge", Passed: true}}, AllowJudge: true, ExecutionSucceeded: true, Cost: .25, Latency: time.Second, Time: time.Unix(100, 0).UTC()}
	if err := s.RecordEvaluation(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	return r
}
func revised(r evaluation.Record, id string, passed bool) evaluation.Record {
	r.ID = id
	r.Checks = []evaluation.Check{{Source: evaluation.UserFeedback, Reference: id, Passed: passed}}
	r.AllowJudge = false
	return r
}

func TestRevisionHistoryRestartAndIdempotency(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "revisions.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	base := revisionBase(t, s)
	next := revised(base, "r1", false)
	if err := s.SupersedeEvaluation(ctx, base.ID, next); err != nil {
		t.Fatal(err)
	}
	next2 := revised(next, "r2", true)
	if err := s.SupersedeEvaluation(ctx, next.ID, next2); err != nil {
		t.Fatal(err)
	}
	if err := s.SupersedeEvaluation(ctx, base.ID, next); err != nil {
		t.Fatal("old retry", err)
	}
	if err := s.SupersedeEvaluation(ctx, base.ID, revised(base, "stale", false)); !errors.Is(err, ErrConflict) {
		t.Fatal("stale accepted", err)
	}
	if err := s.SupersedeEvaluation(ctx, base.ID, revised(base, "r1", true)); !errors.Is(err, ErrConflict) {
		t.Fatal("mutation accepted", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	history, err := s.EvaluationHistory(ctx, "task", "attempt")
	if err != nil || len(history) != 3 || history[0].ID != "base" || history[1].ID != "r1" || history[2].ID != "r2" {
		t.Fatal(history, err)
	}
	current, err := s.CurrentEvaluation(ctx, "task", "attempt")
	if err != nil || current.ID != "r2" {
		t.Fatal(current, err)
	}
	f, err := s.Fitness(ctx, base.Key)
	if err != nil || f.Samples != 1 || f.Quality != 1 || f.Reliability != 1 || f.Cost != base.Cost || f.Latency != base.Latency {
		t.Fatal(f, err)
	}
}

func TestRevisionRollbackAndConcurrentCAS(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "revisions.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	base := revisionBase(t, s)
	if _, err := s.db.Exec(`CREATE TRIGGER fail_revision_fitness BEFORE UPDATE ON fitness BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.SupersedeEvaluation(ctx, base.ID, revised(base, "rollback", false)); err == nil {
		t.Fatal("failure ignored")
	}
	history, err := s.EvaluationHistory(ctx, "task", "attempt")
	if err != nil || len(history) != 1 {
		t.Fatal("partial commit", history, err)
	}
	if _, err := s.db.Exec("DROP TRIGGER fail_revision_fitness"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	other, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	stores := []*Store{s, other}
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results <- stores[i].SupersedeEvaluation(ctx, base.ID, revised(base, fmt.Sprintf("r%d", i), false))
		}(i)
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
	f, err := s.Fitness(ctx, base.Key)
	if err != nil || f.Samples != 1 || f.Quality != 0 {
		t.Fatal(f, err)
	}
}

func TestRevisionMigrationAndBound(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "migration.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	base := revisionBase(t, s)
	if _, err := s.db.Exec("DROP INDEX events_submission_start; DROP TABLE submission_recoveries; DROP TABLE submissions; DROP TABLE task_cancellations; DROP TABLE summary_review_heads; DROP TABLE summary_reviews; DROP TABLE summary_attempts; DROP INDEX events_model_start; DROP TABLE review_attempts; DROP TABLE evaluation_revisions; DROP TABLE evaluation_heads; PRAGMA user_version=5;"); err != nil {
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
	current, err := s.CurrentEvaluation(ctx, "task", "attempt")
	if err != nil || current.ID != base.ID {
		t.Fatal(current, err)
	}
	for i := 0; i < 100; i++ {
		next := revised(current, fmt.Sprintf("revision-%03d", i), i%2 == 0)
		if err := s.SupersedeEvaluation(ctx, current.ID, next); err != nil {
			t.Fatal(i, err)
		}
		current = next
	}
	if err := s.SupersedeEvaluation(ctx, current.ID, revised(current, "excess", false)); !errors.Is(err, evaluation.ErrEvidence) {
		t.Fatal("revision bound", err)
	}
	history, err := s.EvaluationHistory(ctx, "task", "attempt")
	if err != nil || len(history) != 101 {
		t.Fatal(len(history), err)
	}
}
