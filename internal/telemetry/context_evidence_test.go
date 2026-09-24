package telemetry

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/routing"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func appendEvaluationAttempt(t *testing.T, s *Store, task, attempt, model, provider string) {
	t.Helper()
	base := runtime.Event{Version: 1, TaskID: task, SessionID: task, CorrelationID: task, Time: time.Unix(90, 0)}
	start := base
	start.ID, start.Sequence, start.Kind = task+"-start", 1, runtime.TaskStarted
	if err := s.Append(context.Background(), 0, start); err != nil {
		t.Fatal(err)
	}
	turn := base
	turn.ID, turn.Sequence, turn.Kind, turn.TurnID, turn.AttemptID = task+"-turn", 2, runtime.TurnStarted, "turn", attempt
	turn.Data = runtime.Data{ModelID: model, ProviderID: provider}
	if err := s.Append(context.Background(), 1, turn); err != nil {
		t.Fatal(err)
	}
	done := base
	done.ID, done.Sequence, done.Kind, done.TurnID, done.AttemptID = task+"-done", 3, runtime.TurnCompleted, "turn", attempt
	if err := s.Append(context.Background(), 2, done); err != nil {
		t.Fatal(err)
	}
}

func TestContextEvidenceAggregatesTierOutcomes(t *testing.T) {
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	for i, accepted := range []bool{true, false} {
		task, attempt := "context-task-"+string(rune('a'+i)), "attempt"
		appendEvaluationAttempt(t, s, task, attempt, "model", "provider")
		record := evaluation.Record{Version: 1, ID: task + "-evaluation", TaskID: task, AttemptID: attempt, Key: routing.Key{Model: "model", Provider: "provider", Domain: "code", Profile: "default"}, Checks: []evaluation.Check{{Source: evaluation.Deterministic, Reference: task, Passed: accepted}}, ExecutionSucceeded: true, ContextTokens: 32768, Latency: time.Duration(i+1) * time.Second, PeakMemoryBytes: uint64(i+1) * 100, SwapGrowthBytes: uint64(i) * 10, Time: time.Unix(int64(100+i), 0)}
		if err := s.RecordEvaluation(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ContextEvidence(ctx, "model", "provider")
	if err != nil || len(got) != 1 || got[0].Samples != 2 || got[0].Quality != .5 || got[0].LatencyMillis != 1500 || got[0].PeakMemory != 200 || got[0].MaxSwapGrowth != 10 {
		t.Fatalf("evidence=%+v err=%v", got, err)
	}
	scoped, err := s.ContextEvidenceFor(ctx, routing.Key{Model: "model", Provider: "provider", Domain: "math", Profile: "default"})
	if err != nil || len(scoped) != 1 || scoped[0].Samples != 0 || scoped[0].Quality != 0 || scoped[0].MaxSwapGrowth != 10 {
		t.Fatalf("unrelated accuracy leaked or safety lost: %+v %v", scoped, err)
	}
	scoped, err = s.ContextEvidenceFor(ctx, routing.Key{Model: "model", Provider: "provider", Domain: "code", Profile: "default"})
	if err != nil || len(scoped) != 1 || scoped[0].Samples != 2 || scoped[0].Quality != .5 {
		t.Fatalf("matching evidence lost: %+v %v", scoped, err)
	}
}
