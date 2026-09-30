package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/routing"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func TestFitnessFailureRemainsAtomicAfterRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "fitness-restart.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	for i, kind := range []runtime.Kind{runtime.TaskStarted, runtime.TurnStarted, runtime.TurnCompleted} {
		e := event(string(kind), int64(i+1), kind)
		e.TurnID, e.AttemptID = "turn", "attempt"
		e.Data = runtime.Data{ModelID: "model", ProviderID: "provider"}
		if err := store.Append(ctx, int64(i), e); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.Exec(`CREATE TRIGGER fail_fitness_restart BEFORE INSERT ON fitness BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	key := routing.Key{Model: "model", Provider: "provider", Domain: "code", Profile: "default"}
	record := evaluation.Record{Version: 1, ID: "evaluation", TaskID: "task", AttemptID: "attempt", Key: key, Checks: []evaluation.Check{{Source: evaluation.Deterministic, Reference: "tests", Passed: true}}, ExecutionSucceeded: true, Time: time.Unix(100, 0)}
	if err := store.RecordEvaluation(ctx, record); err == nil {
		t.Fatal("injected fitness failure committed")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	history, err := reopened.EvaluationHistory(ctx, record.TaskID, record.AttemptID)
	if !errors.Is(err, sql.ErrNoRows) || len(history) != 0 {
		t.Fatalf("evaluation escaped rollback after restart: %+v %v", history, err)
	}
	if _, err := reopened.Fitness(ctx, key); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("fitness escaped rollback after restart: %v", err)
	}
}

func TestReviewedCompactionStartRollbackSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "compaction-restart.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	attempt, review := reviewDraftFixture(t, store)
	if err := store.RecordSummaryReview(ctx, review); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER fail_compaction_start BEFORE INSERT ON events WHEN NEW.task_id='continuation' BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(ctx, 0, reviewedStart(t, store, attempt, review, "continuation")); err == nil {
		t.Fatal("injected compaction-start failure committed")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	for _, table := range []string{"task_heads", "events"} {
		var count int
		query := "SELECT count(*) FROM " + table + " WHERE task_id='continuation'"
		if err := reopened.db.QueryRowContext(ctx, query).Scan(&count); err != nil || count != 0 {
			t.Fatalf("partial %s survived restart: count=%d err=%v", table, count, err)
		}
	}
	current, err := reopened.CurrentSummaryReview(ctx, attempt.ID)
	if err != nil || current.ID != review.ID || current.Decision != "approved" {
		t.Fatalf("source review changed by rolled-back continuation: %+v %v", current, err)
	}
}
