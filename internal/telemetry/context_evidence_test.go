package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/routing"
	"github.com/ArronJablonowski/NexusRouter/runtime"
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

func contextTestRecord(t *testing.T, s *Store, task string) evaluation.Record {
	t.Helper()
	appendEvaluationAttempt(t, s, task, "attempt", "model", "provider")
	return evaluation.Record{Version: 1, ID: task + "-evaluation", TaskID: task, AttemptID: "attempt", Key: routing.Key{Model: "model", Provider: "provider", Domain: "code", Profile: "default"}, Checks: []evaluation.Check{{Source: evaluation.UserFeedback, Reference: task, Passed: true}}, ExecutionSucceeded: true, ContextTokens: 32768, Latency: time.Second, Time: time.Unix(100, 0)}
}

func TestContextEvidenceSeparatesExecutionFaultsFromAccuracy(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < 4; i++ {
		record := contextTestRecord(t, s, "task-"+string(rune('a'+i)))
		if i > 0 {
			record.Checks[0].Passed = false
			record.Latency = 100 * time.Second
		}
		record.ExecutionSucceeded = i != 1
		record.TimedOut = i == 2
		record.ProviderError = i == 3
		if err := s.RecordEvaluation(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	evidence, err := s.ContextEvidenceFor(ctx, routing.Key{Model: "model", Provider: "provider", Domain: "code", Profile: "default"})
	if err != nil || len(evidence) != 1 || evidence[0].Samples != 1 || evidence[0].Quality != 1 || evidence[0].LatencyMillis != 1000 || evidence[0].Timeouts != 1 || evidence[0].ProviderErrors != 1 {
		t.Fatalf("execution faults polluted accuracy or lost safety: %+v %v", evidence, err)
	}
}

func TestContextEvidenceRejectsMissingOrMismatchedHeads(t *testing.T) {
	for _, damage := range []string{"missing-head", "other-attempt-head", "mismatched-body", "changed-context"} {
		t.Run(damage, func(t *testing.T) {
			ctx := context.Background()
			s, err := Open(ctx, filepath.Join(t.TempDir(), "context.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			base := contextTestRecord(t, s, "task-a")
			if err = s.RecordEvaluation(ctx, base); err != nil {
				t.Fatal(err)
			}
			switch damage {
			case "missing-head":
				_, err = s.db.Exec(`DELETE FROM evaluation_heads WHERE base_id=?`, base.ID)
			case "mismatched-body":
				base.Key.Model = "another-model"
				body, _ := json.Marshal(base)
				_, err = s.db.Exec(`UPDATE evaluations SET body=? WHERE id=?`, body, base.ID)
			default:
				other := contextTestRecord(t, s, "task-b")
				if err = s.RecordEvaluation(ctx, other); err != nil {
					t.Fatal(err)
				}
				revision := other
				if damage == "changed-context" {
					revision = base
				}
				revision.ID = "correction"
				revision.Checks = []evaluation.Check{{Source: evaluation.UserFeedback, Reference: "correction", Passed: false}}
				if err = s.SupersedeEvaluation(ctx, revision.TaskID+"-evaluation", revision); err != nil {
					t.Fatal(err)
				}
				if damage == "changed-context" {
					revision.ContextTokens = 65536
					body, _ := json.Marshal(revision)
					_, err = s.db.Exec(`UPDATE evaluation_revisions SET body=? WHERE id=?`, body, revision.ID)
				} else {
					_, err = s.db.Exec(`UPDATE evaluation_heads SET current_id=base_id WHERE base_id=?`, other.ID)
					if err == nil {
						_, err = s.db.Exec(`UPDATE evaluation_heads SET current_id=? WHERE base_id=?`, revision.ID, base.ID)
					}
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if evidence, err := s.ContextEvidence(ctx, "model", "provider"); !errors.Is(err, evaluation.ErrEvidence) || evidence != nil {
				t.Fatalf("damaged evidence accepted: %+v %v", evidence, err)
			}
		})
	}
}

func TestContextEvidenceKeepsCurrentCorrectionAsOneSample(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	base := contextTestRecord(t, s, "task")
	base.PeakMemoryBytes, base.SwapGrowthBytes = 200, 10
	if err = s.RecordEvaluation(ctx, base); err != nil {
		t.Fatal(err)
	}
	prior := base
	for _, verdict := range []bool{false, true, false} {
		next := revised(prior, prior.ID+"-revision", verdict)
		if err = s.SupersedeEvaluation(ctx, prior.ID, next); err != nil {
			t.Fatal(err)
		}
		prior = next
	}
	got, err := s.ContextEvidenceFor(ctx, base.Key)
	if err != nil || len(got) != 1 || got[0].Samples != 1 || got[0].Quality != 0 || got[0].LatencyMillis != 1000 || got[0].PeakMemory != 200 || got[0].MaxSwapGrowth != 10 {
		t.Fatalf("correction changed measurements or sample count: %+v %v", got, err)
	}
	otherProfile := base.Key
	otherProfile.Profile = "benchmark"
	got, err = s.ContextEvidenceFor(ctx, otherProfile)
	if err != nil || len(got) != 1 || got[0].Samples != 0 || got[0].PeakMemory != 200 || got[0].MaxSwapGrowth != 10 {
		t.Fatalf("profile leaked accuracy or lost shared resources: %+v %v", got, err)
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
