package telemetry

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/routing"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func workflowSourceFixture(t *testing.T, s *Store, task, session, domain string, terminal runtime.Kind, source evaluation.Source, passed, record bool) evaluation.Record {
	t.Helper()
	ctx := context.Background()
	for i, kind := range []runtime.Kind{runtime.TaskStarted, runtime.TurnStarted, runtime.TurnCompleted, terminal} {
		e := runtime.Event{Version: 1, ID: task + "-" + string(kind), TaskID: task, SessionID: session, CorrelationID: task, Sequence: int64(i + 1), Time: time.Unix(100, 0).UTC(), Kind: kind}
		if i > 0 {
			e.TurnID = task + "-turn"
			e.AttemptID = task + "-attempt"
		}
		if kind == runtime.TaskStarted {
			e.Data = runtime.Data{ModelID: "model", ProviderID: "provider", Domain: domain, Profile: "default", Privacy: "local_only", Messages: []providers.Message{{Role: "system", Content: "private-system-instruction"}, {Role: "user", Content: "private-source-request"}}}
		}
		if kind == runtime.TurnStarted {
			e.Data = runtime.Data{ModelID: "model", ProviderID: "provider"}
		}
		if kind == runtime.TurnCompleted {
			e.Data = runtime.Data{Text: "observed answer", FinishReason: "stop"}
		}
		if err := s.Append(ctx, int64(i), e); err != nil {
			t.Fatal(err)
		}
	}
	r := evaluation.Record{Version: 1, ID: task + "-evaluation", TaskID: task, AttemptID: task + "-attempt", Key: routing.Key{Model: "model", Provider: "provider", Domain: domain, Profile: "default"}, Checks: []evaluation.Check{{Source: source, Reference: task + "-check", Passed: passed}}, AllowJudge: source == evaluation.LLMJudge, ExecutionSucceeded: true, Time: time.Unix(101, 0).UTC()}
	if record {
		if err := s.RecordEvaluation(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func TestSkillWorkflowSourcesCurrentEvidenceAndReadOnlyNonmutation(t *testing.T) {
	s, path := generationStore(t)
	ctx := context.Background()
	a := workflowSourceFixture(t, s, "task-a", "session-a", "creative", runtime.TaskCompleted, evaluation.Deterministic, true, true)
	b := workflowSourceFixture(t, s, "task-b", "session-b", "creative", runtime.TaskCompleted, evaluation.UserFeedback, true, true)
	beforeA, err := s.Read(ctx, a.TaskID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	beforeB, err := s.Read(ctx, b.TaskID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	beforeBytes := workflowSourceRawBodies(t, s)
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	sources, err := ro.SkillWorkflowSources(ctx, []string{a.TaskID, b.TaskID})
	if err != nil || len(sources) != 2 {
		t.Fatal(sources, err)
	}
	for i, src := range sources {
		want := []evaluation.Record{a, b}[i]
		if src.Example.TaskID != want.TaskID || src.Example.SessionID != "session-"+string(rune('a'+i)) || src.Example.Domain != "creative" || src.Privacy != "local_only" || src.EvaluationID != want.ID || src.SourceSequence != 4 || len(src.Example.Checks) != 1 || !src.Example.Checks[0].Passed || src.Example.Checks[0].Source != want.Checks[0].Source {
			t.Fatalf("wrong source attribution: %+v", src)
		}
		for _, digest := range []string{src.EvaluationDigest, src.SourceDigest, src.Example.Checks[0].Reference} {
			decoded, err := hex.DecodeString(digest)
			if err != nil || len(decoded) != 32 || len(digest) != 64 {
				t.Fatal("unbounded/noncanonical evidence", digest, err)
			}
		}
		joined := strings.Join(src.Example.Steps, "\n")
		if strings.Contains(joined, "private-system-instruction") || !strings.Contains(joined, "private-source-request") || !strings.Contains(joined, "observed answer") {
			t.Fatal("source transcript incorrectly projected/redacted", joined)
		}
		for _, step := range src.Example.Steps {
			if !json.Valid([]byte(step)) {
				t.Fatal("observation is not structured JSON", step)
			}
		}
	}
	afterA, err := s.Read(ctx, a.TaskID, 0, 100)
	if err != nil || !reflect.DeepEqual(beforeA, afterA) {
		t.Fatal("source A modified", err)
	}
	afterB, err := s.Read(ctx, b.TaskID, 0, 100)
	if err != nil || !reflect.DeepEqual(beforeB, afterB) {
		t.Fatal("source B modified", err)
	}
	if !reflect.DeepEqual(beforeBytes, workflowSourceRawBodies(t, s)) {
		t.Fatal("read-only extraction changed raw journal/evaluation bytes")
	}
	// Latest subjective evidence wins over an older accepted evaluation.
	rejected := b
	rejected.ID = "user-rejection"
	rejected.Checks = []evaluation.Check{{Source: evaluation.UserFeedback, Reference: "negative-feedback", Passed: false}}
	if err := s.SupersedeEvaluation(ctx, b.ID, rejected); err != nil {
		t.Fatal(err)
	}
	if got, err := ro.SkillWorkflowSources(ctx, []string{a.TaskID, b.TaskID}); err == nil || len(got) != 0 {
		t.Fatal("older acceptance overrode current rejection", got, err)
	}
	accepted := rejected
	accepted.ID = "user-correction"
	accepted.Checks = []evaluation.Check{{Source: evaluation.UserFeedback, Reference: "positive-correction", Passed: true}}
	if err := s.SupersedeEvaluation(ctx, rejected.ID, accepted); err != nil {
		t.Fatal(err)
	}
	current, err := ro.SkillWorkflowSources(ctx, []string{a.TaskID, b.TaskID})
	if err != nil || len(current) != 2 || current[1].EvaluationID != accepted.ID || current[1].EvaluationDigest == sources[1].EvaluationDigest {
		t.Fatal("current correction not selected", current, err)
	}
}

func workflowSourceRawBodies(t *testing.T, s *Store) []string {
	t.Helper()
	out := []string{}
	for _, query := range []string{`SELECT body FROM events ORDER BY task_id,sequence`, `SELECT body FROM evaluations ORDER BY id`} {
		rows, err := s.db.Query(query)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var body string
			if err := rows.Scan(&body); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			out = append(out, body)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func TestSkillWorkflowSourcesRejectsUnqualifiedInputs(t *testing.T) {
	for _, mode := range []string{"noevaluation", "judgeonly", "objectivefailure", "failed", "canceled", "duplicate_tasks", "duplicate_sessions", "mixed_domain", "wrong_model", "wrong_attempt"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := generationStore(t)
			ctx := context.Background()
			workflowSourceFixture(t, s, "task-a", "session-a", "creative", runtime.TaskCompleted, evaluation.Deterministic, true, true)
			session, domain := "session-b", "creative"
			terminal := runtime.TaskCompleted
			source := evaluation.UserFeedback
			passed, record := true, true
			switch mode {
			case "noevaluation":
				record = false
			case "judgeonly":
				source = evaluation.LLMJudge
			case "objectivefailure":
				source = evaluation.Deterministic
				passed = false
			case "failed":
				terminal = runtime.TaskFailed
			case "canceled":
				terminal = runtime.TaskCanceled
			case "duplicate_sessions":
				session = "session-a"
			case "mixed_domain":
				domain = "code"
			}
			b := workflowSourceFixture(t, s, "task-b", session, domain, terminal, source, passed, record)
			if mode == "wrong_model" || mode == "wrong_attempt" {
				column := "model"
				value := "different"
				if mode == "wrong_model" {
					b.Key.Model = value
				} else {
					column = "attempt_id"
					b.AttemptID = value
				}
				body, err := json.Marshal(b)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = s.db.Exec("UPDATE evaluations SET "+column+"=?,body=? WHERE id=?", value, body, b.ID); err != nil {
					t.Fatal(err)
				}
			}
			tasks := []string{"task-a", "task-b"}
			if mode == "duplicate_tasks" {
				tasks[1] = "task-a"
			}
			got, err := s.SkillWorkflowSources(ctx, tasks)
			if err == nil || len(got) != 0 {
				t.Fatal("unqualified source admitted", mode, got, err)
			}
		})
	}
}

func TestSkillWorkflowSourcesPreservesPairedToolObservations(t *testing.T) {
	s, _ := generationStore(t)
	ctx := context.Background()
	workflowSourceFixture(t, s, "task-a", "session-a", "code", runtime.TaskCompleted, evaluation.Deterministic, true, true)
	task := "task-tools"
	kinds := []runtime.Kind{runtime.TaskStarted, runtime.TurnStarted, runtime.TurnCompleted, runtime.ToolStarted, runtime.ToolCompleted, runtime.TurnStarted, runtime.TurnCompleted, runtime.TaskCompleted}
	for i, kind := range kinds {
		e := runtime.Event{Version: 1, ID: fmt.Sprintf("tools-event-%d", i), TaskID: task, SessionID: "session-tools", CorrelationID: task, Sequence: int64(i + 1), Time: time.Unix(100, 0).UTC(), Kind: kind}
		if i > 0 {
			e.TurnID = "tool-turn"
			e.AttemptID = "tool-attempt"
		}
		if i >= 5 {
			e.TurnID = "final-turn"
			e.AttemptID = "final-attempt"
		}
		switch i {
		case 0:
			e.Data = runtime.Data{ModelID: "model", ProviderID: "provider", Domain: "code", Profile: "default", Privacy: "local_only", Messages: []providers.Message{{Role: "user", Content: "inspect evidence"}}}
		case 1, 5:
			e.Data = runtime.Data{ModelID: "model", ProviderID: "provider"}
		case 2:
			e.Data = runtime.Data{ToolCalls: []providers.ToolCall{{ID: "paired-call", Name: "lookup", Arguments: json.RawMessage(`{"key":"proof"}`)}}, FinishReason: "tool_calls"}
		case 3:
			e.Data = runtime.Data{ToolCallID: "paired-call", ToolName: "lookup", Effect: runtime.UncertainEffect}
		case 4:
			e.Data = runtime.Data{ToolCallID: "paired-call", ToolName: "lookup", Effect: runtime.NoEffect, Text: "tool evidence"}
		case 6:
			e.Data = runtime.Data{Text: "checked answer", FinishReason: "stop"}
		}
		if err := s.Append(ctx, int64(i), e); err != nil {
			t.Fatal(err)
		}
	}
	r := evaluation.Record{Version: 1, ID: "tools-evaluation", TaskID: task, AttemptID: "final-attempt", Key: routing.Key{Model: "model", Provider: "provider", Domain: "code", Profile: "default"}, Checks: []evaluation.Check{{Source: evaluation.ToolResult, Reference: "tool-validation", Passed: true}}, ExecutionSucceeded: true, Time: time.Unix(101, 0).UTC()}
	if err := s.RecordEvaluation(ctx, r); err != nil {
		t.Fatal(err)
	}
	sources, err := s.SkillWorkflowSources(ctx, []string{"task-a", task})
	if err != nil || len(sources) != 2 {
		t.Fatal(sources, err)
	}
	joined := strings.Join(sources[1].Example.Steps, "\n")
	if !strings.Contains(joined, "paired-call") || !strings.Contains(joined, "tool evidence") || !strings.Contains(joined, "checked answer") || !strings.Contains(joined, "lookup") || sources[1].SourceSequence != 8 {
		t.Fatal("tool observations lost", joined)
	}
}
