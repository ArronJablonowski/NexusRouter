package telemetry

import (
	"context"
	"encoding/json"
	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"path/filepath"
	"testing"
)

func TestObjectiveCorrectionPreservesHistoryAndSingleSample(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	base := revisionBase(t, s)
	// An LLM grade cannot be relabeled objective, even after user feedback.
	subjective := revised(base, "subjective", true)
	if err = s.SupersedeEvaluation(ctx, base.ID, subjective); err != nil {
		t.Fatal(err)
	}
	objective := subjective
	objective.ID = "objective"
	objective.Checks = []evaluation.Check{{Source: evaluation.Deterministic, Reference: "benchmark:sha256:fixture", Passed: true}}
	if s.CorrectObjectiveAttribution(ctx, subjective.ID, objective) == nil {
		t.Fatal("judge base relabeled")
	}
	s.Close()
	// Fresh original user feedback, matching this campaign's attribution error.
	s, err = Open(ctx, filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	base = revisionBase(t, s)
	// Fixture-only update before exercising production APIs.
	base.Checks = []evaluation.Check{{Source: evaluation.UserFeedback, Reference: "original", Passed: true}}
	base.AllowJudge = false
	raw := mustObjectiveJSON(t, base)
	if _, err = s.db.Exec("UPDATE evaluations SET body=? WHERE id=?", raw, base.ID); err != nil {
		t.Fatal(err)
	}
	objective = base
	objective.ID = "objective"
	objective.Checks = []evaluation.Check{{Source: evaluation.Deterministic, Reference: "benchmark:sha256:fixture", Passed: true}}
	if s.SupersedeEvaluation(ctx, base.ID, objective) == nil {
		t.Fatal("subjective API admitted objective")
	}
	bad := objective
	bad.Cost++
	if s.CorrectObjectiveAttribution(ctx, base.ID, bad) == nil {
		t.Fatal("cost changed")
	}
	bad = objective
	bad.Checks = []evaluation.Check{{Source: evaluation.Deterministic, Reference: "other", Passed: false}}
	if s.CorrectObjectiveAttribution(ctx, base.ID, bad) == nil {
		t.Fatal("verdict changed")
	}
	for i := 0; i < 2; i++ {
		if err = s.CorrectObjectiveAttribution(ctx, base.ID, objective); err != nil {
			t.Fatal(err)
		}
	}
	history, err := s.EvaluationHistory(ctx, base.TaskID, base.AttemptID)
	if err != nil || len(history) != 2 {
		t.Fatalf("history %d %v", len(history), err)
	}
	evidence, err := s.Fitness(ctx, base.Key)
	if err != nil || evidence.Samples != 1 {
		t.Fatalf("samples %v %v", evidence, err)
	}
	if s.SupersedeEvaluation(ctx, objective.ID, revised(objective, "downgrade", false)) == nil {
		t.Fatal("objective downgraded")
	}
	if _, err = s.DirectObservationSet(ctx, base.Key); err != nil {
		t.Fatal(err)
	}
}

func mustObjectiveJSON(t *testing.T, r evaluation.Record) []byte {
	t.Helper()
	b, e := json.Marshal(r)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
