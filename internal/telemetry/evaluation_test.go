package telemetry

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/routing"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func TestAtomicEvaluationAndReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "evaluation.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Append(ctx, 0, event("start", 1, runtime.TaskStarted)); err != nil {
		t.Fatal(err)
	}
	turn := event("turn", 2, runtime.TurnStarted)
	turn.TurnID = "turn"
	turn.AttemptID = "attempt"
	turn.Data = runtime.Data{ModelID: "model", ProviderID: "provider"}
	if err := s.Append(ctx, 1, turn); err != nil {
		t.Fatal(err)
	}
	schemaPassed := true
	r := evaluation.Record{Version: 1, ID: "eval", TaskID: "task", AttemptID: "attempt", Key: routing.Key{Model: "model", Provider: "provider", Domain: "code", Profile: "default"}, Checks: []evaluation.Check{{Source: evaluation.Deterministic, Reference: "test-id", Passed: true}, {Source: evaluation.ToolResult, Reference: "tool-receipt", Passed: true}}, SchemaPassed: &schemaPassed, ExecutionSucceeded: true, Latency: time.Second, Cost: .1, Time: time.Unix(100, 0)}
	if err := s.RecordEvaluation(ctx, r); !errors.Is(err, evaluation.ErrEvidence) {
		t.Fatal("unfinished turn evaluated", err)
	}
	done := event("turn-done", 3, runtime.TurnCompleted)
	done.TurnID = "turn"
	done.AttemptID = "attempt"
	if err := s.Append(ctx, 2, done); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordEvaluation(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordEvaluation(ctx, r); err != nil {
		t.Fatal("retry failed", err)
	}
	changed := r
	changed.Checks = []evaluation.Check{{Source: evaluation.Deterministic, Reference: "test-id", Passed: false}}
	if err := s.RecordEvaluation(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("mutated record accepted", err)
	}
	changed = r
	changed.ID = "other"
	if err := s.RecordEvaluation(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("duplicate attempt counted", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e, err := s.Fitness(ctx, r.Key)
	if err != nil || e.Samples != 1 || e.Quality != 1 || e.Reliability != 1 || e.Compliance != 1 || e.Latency != time.Second || e.Cost != .1 {
		t.Fatalf("%+v %v", e, err)
	}
	history, err := s.EvaluationHistory(ctx, r.TaskID, r.AttemptID)
	if err != nil || len(history) != 1 || history[0].SchemaPassed == nil || !*history[0].SchemaPassed || len(history[0].Checks) != 2 || history[0].Checks[1].Source != evaluation.ToolResult {
		t.Fatalf("objective evidence history lost: %+v %v", history, err)
	}
	var records int
	if err := s.db.QueryRow("SELECT count(*) FROM evaluations").Scan(&records); err != nil || records != 1 {
		t.Fatalf("records=%d %v", records, err)
	}
}

func TestFitnessFailureRollsBackEvidence(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "failure.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i, k := range []runtime.Kind{runtime.TaskStarted, runtime.TurnStarted, runtime.TurnCompleted} {
		e := event(string(k), int64(i+1), k)
		e.TurnID = "turn"
		e.AttemptID = "attempt"
		e.Data = runtime.Data{ModelID: "m", ProviderID: "p"}
		if err := s.Append(ctx, int64(i), e); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`CREATE TRIGGER fail_fitness BEFORE INSERT ON fitness BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	r := evaluation.Record{Version: 1, ID: "eval", TaskID: "task", AttemptID: "attempt", Key: routing.Key{Model: "m", Provider: "p", Domain: "d", Profile: "p"}, Checks: []evaluation.Check{{Source: evaluation.ToolResult, Reference: "check", Passed: false}}, Time: time.Unix(100, 0)}
	if err := s.RecordEvaluation(ctx, r); err == nil {
		t.Fatal("injected failure ignored")
	}
	var n int
	if err := s.db.QueryRow("SELECT count(*) FROM evaluations").Scan(&n); err != nil || n != 0 {
		t.Fatal("partial commit", n, err)
	}
}
