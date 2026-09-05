package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/routing"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

func procedureFixture(t *testing.T, s *Store, task, code string, effect runtime.Effect) evaluation.Record {
	t.Helper()
	ctx := context.Background()
	kinds := []runtime.Kind{runtime.TaskStarted, runtime.TurnStarted, runtime.TurnCompleted, runtime.ToolStarted, runtime.ToolCompleted, runtime.ToolStarted, runtime.ToolCompleted, runtime.ToolStarted, runtime.ToolCompleted, runtime.TurnStarted, runtime.TurnCompleted, runtime.TaskCompleted}
	if effect == runtime.UncertainEffect {
		kinds[len(kinds)-1] = runtime.TaskFailed
	}
	names := []string{"read_file", "run_tests", "read_file"}
	for i, kind := range kinds {
		e := runtime.Event{Version: 1, ID: fmt.Sprintf("%s-e-%d", task, i), TaskID: task, SessionID: task + "-session", CorrelationID: task, Sequence: int64(i + 1), Time: time.Unix(100, 0).UTC(), Kind: kind}
		if i > 0 {
			e.TurnID = task + "-turn"
			e.AttemptID = task + "-attempt"
		}
		if i >= 9 {
			e.TurnID = task + "-final-turn"
			e.AttemptID = task + "-final-attempt"
		}
		switch {
		case i == 0:
			e.Data = runtime.Data{Domain: "code", Profile: "default", ModelID: "model", ProviderID: "provider", Privacy: "local_only", Messages: []providers.Message{{Role: "user", Content: "inspect"}}}
		case i == 1 || i == 9:
			e.Data = runtime.Data{ModelID: "model", ProviderID: "provider"}
		case i == 2:
			for j, name := range names {
				e.Data.ToolCalls = append(e.Data.ToolCalls, providers.ToolCall{ID: fmt.Sprintf("%s-call-%d", task, j), Name: name, Arguments: json.RawMessage(`{}`)})
			}
			e.Data.FinishReason = "tool_calls"
		case i >= 3 && i <= 8:
			j := (i - 3) / 2
			e.Data = runtime.Data{ToolCallID: fmt.Sprintf("%s-call-%d", task, j), ToolName: names[j], Effect: runtime.UncertainEffect}
			if kind == runtime.ToolCompleted {
				e.Data.Effect = effect
				e.Data.Text = "observed result"
				if j == 1 {
					e.Data.Code = code
				}
			}
		case i == 10:
			e.Data = runtime.Data{Text: "validated response", FinishReason: "stop"}
		}
		if err := s.Append(ctx, int64(i), e); err != nil {
			t.Fatal(err)
		}
	}
	r := evaluation.Record{Version: 1, ID: task + "-evaluation", TaskID: task, AttemptID: task + "-final-attempt", Key: routing.Key{Model: "model", Provider: "provider", Domain: "code", Profile: "default"}, Checks: []evaluation.Check{{Source: evaluation.UserFeedback, Reference: task + "-check", Passed: true}}, ExecutionSucceeded: true, Time: time.Unix(101, 0).UTC()}
	if err := s.RecordEvaluation(ctx, r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestWorkflowProceduresActualToolsAndReadOnlyProvenance(t *testing.T) {
	s, path := generationStore(t)
	ctx := context.Background()
	a := procedureFixture(t, s, "task-a", "", runtime.NoEffect)
	workflowSourceFixture(t, s, "task-b", "session-b", "code", runtime.TaskCompleted, evaluation.UserFeedback, true, true)
	// Imported, paired tool history is valid conversation data, not execution.
	events, err := s.Read(ctx, "task-b", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	e := events[0]
	e.Data.Messages = append(e.Data.Messages, providers.Message{Role: "assistant", ToolCalls: []providers.ToolCall{{ID: "imported", Name: "shell", Arguments: json.RawMessage(`{}`)}}}, providers.Message{Role: "tool", ToolCallID: "imported", Content: "claimed shell success"})
	body, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE events SET body=? WHERE task_id='task-b' AND sequence=1`, string(body)); err != nil {
		t.Fatal(err)
	}
	before := workflowSourceRawBodies(t, s)
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	got, err := ro.SkillWorkflowProcedures(ctx, []string{"task-b", "task-a"})
	if err != nil || len(got) != 2 {
		t.Fatal(got, err)
	}
	if !reflect.DeepEqual(got[0].Tools, []string{"read_file", "run_tests", "read_file"}) || got[1].Tools == nil || len(got[1].Tools) != 0 {
		t.Fatal("injected or reordered tool evidence", got)
	}
	sources, err := ro.SkillWorkflowSources(ctx, []string{"task-a", "task-b"})
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range got {
		if p.Profile != "default" || p.Candidate.SourceDigest != sources[i].SourceDigest || p.Candidate.EvaluationDigest != sources[i].EvaluationDigest || p.Candidate.SourceSequence != sources[i].SourceSequence {
			t.Fatal("wrong source binding", p)
		}
	}
	if !reflect.DeepEqual(before, workflowSourceRawBodies(t, s)) {
		t.Fatal("inspection mutated history")
	}
	revision := a
	revision.ID = "negative"
	revision.Checks = []evaluation.Check{{Source: evaluation.UserFeedback, Reference: "negative-check", Passed: false}}
	if err = s.SupersedeEvaluation(ctx, a.ID, revision); err != nil {
		t.Fatal(err)
	}
	got, err = ro.SkillWorkflowProcedures(ctx, []string{"task-a", "task-b"})
	if err != nil || len(got) != 1 || got[0].Candidate.TaskID != "task-b" {
		t.Fatal("stale evaluation used", got, err)
	}
}

func TestWorkflowProceduresRejectFailedTrajectoryAndCorruption(t *testing.T) {
	for _, mode := range []string{"failed-tool", "uncertain", "corrupt"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := generationStore(t)
			ctx := context.Background()
			code := ""
			effect := runtime.NoEffect
			if mode == "failed-tool" {
				code = "tool_failed"
			}
			if mode == "uncertain" {
				effect = runtime.UncertainEffect
			}
			procedureFixture(t, s, "task-a", code, effect)
			procedureFixture(t, s, "task-b", "", runtime.NoEffect)
			if mode == "corrupt" {
				if _, err := s.db.Exec(`UPDATE events SET body='{}' WHERE task_id='task-b' AND sequence=2`); err != nil {
					t.Fatal(err)
				}
			}
			got, err := s.SkillWorkflowProcedures(ctx, []string{"task-a", "task-b"})
			if mode == "corrupt" {
				if err == nil || got != nil {
					t.Fatal("partial corruption result", got, err)
				}
				return
			}
			if err != nil || len(got) != 1 || got[0].Candidate.TaskID != "task-b" {
				t.Fatal("failed trajectory admitted", got, err)
			}
		})
	}
}

func TestWorkflowProceduresInputGuards(t *testing.T) {
	s, _ := generationStore(t)
	for _, ids := range [][]string{nil, {"duplicate", "duplicate"}, {"bad.id"}, make([]string, 21)} {
		if got, err := s.SkillWorkflowProcedures(context.Background(), ids); err == nil || got != nil {
			t.Fatal("invalid input accepted", got, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, ctx := range []context.Context{nil, ctx} {
		if got, err := s.SkillWorkflowProcedures(ctx, []string{"task"}); err == nil || got != nil {
			t.Fatal(got, err)
		}
	}
	var absent *Store
	if _, err := absent.SkillWorkflowProcedures(context.Background(), []string{"task"}); err == nil {
		t.Fatal("nil store accepted")
	}
}

func TestWorkflowGroupSourcesRechecksActualExecution(t *testing.T) {
	s, _ := generationStore(t)
	ctx := context.Background()
	procedureFixture(t, s, "task-a", "", runtime.NoEffect)
	procedureFixture(t, s, "task-b", "", runtime.NoEffect)
	ids := []string{"task-a", "task-b"}
	procedures, err := s.SkillWorkflowProcedures(ctx, ids)
	if err != nil {
		t.Fatal(err)
	}
	groups, err := skills.BuildWorkflowGroups(procedures)
	if err != nil || len(groups) != 1 {
		t.Fatal(groups, err)
	}
	sources, err := s.SkillWorkflowGroupSources(ctx, ids, groups[0].ID)
	if err != nil || len(sources) != 2 {
		t.Fatal(sources, err)
	}
	if _, err = s.SkillWorkflowGroupSources(ctx, ids, "invalid"); err == nil {
		t.Fatal("invalid group accepted")
	}
	events, err := s.Read(ctx, "task-b", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	e := events[6]
	e.Data.Code = "tool_failed"
	body, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE events SET body=? WHERE task_id='task-b' AND sequence=7`, string(body)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SkillWorkflowSources(ctx, ids); err != nil {
		t.Fatal("accepted task source should still be inspectable", err)
	}
	if got, err := s.SkillWorkflowGroupSources(ctx, ids, groups[0].ID); err == nil || got != nil {
		t.Fatal("stale actual execution grouping accepted", got, err)
	}
}
