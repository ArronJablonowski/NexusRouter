package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func TestSkillTaskOutcomeCurrentQualityAndUnknown(t *testing.T) {
	s, path := generationStore(t)
	ctx := context.Background()
	r := workflowSourceFixture(t, s, "creative", "session", "creative", runtime.TaskCompleted, evaluation.LLMJudge, true, true)
	for _, terminal := range []runtime.Kind{runtime.TaskFailed, runtime.TaskCanceled} {
		workflowSourceFixture(t, s, string(terminal), string(terminal), "code", terminal, evaluation.UserFeedback, false, true)
	}
	workflowSourceFixture(t, s, "unknown", "unknown", "creative", runtime.TaskCompleted, evaluation.UserFeedback, true, false)
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	before := workflowSourceRawBodies(t, s)
	first, err := ro.SkillTaskOutcome(ctx, r.TaskID)
	if err != nil || first.Quality == nil || first.Quality.Source != evaluation.LLMJudge || !first.Quality.Accepted || first.SkillContext != nil || first.Key.Domain != "creative" || first.Key.Profile != "default" {
		t.Fatal(first, err)
	}
	next := r
	next.ID = "revision"
	next.AllowJudge = false
	next.Checks = []evaluation.Check{{Source: evaluation.UserFeedback, Reference: "user", Passed: false}}
	if err = s.SupersedeEvaluation(ctx, r.ID, next); err != nil {
		t.Fatal(err)
	}
	after, err := ro.SkillTaskOutcome(ctx, r.TaskID)
	if err != nil || after.Quality == nil || after.Quality.Accepted || after.Quality.Source != evaluation.UserFeedback || after.EvaluationID != next.ID || after.EvaluationDigest == first.EvaluationDigest {
		t.Fatal(after, err)
	}
	for _, task := range []string{"task.failed", "task.canceled"} {
		out, err := ro.SkillTaskOutcome(ctx, task)
		if err != nil || out.Quality == nil || out.Quality.Accepted {
			t.Fatal(out, err)
		}
	}
	unknown, err := ro.SkillTaskOutcome(ctx, "unknown")
	if err != nil || unknown.Quality != nil || unknown.EvaluationID != "" || len(unknown.OutputChecks) != 0 {
		t.Fatal(unknown, err)
	}
	if !reflect.DeepEqual(before, workflowSourceRawBodies(t, s)) {
		t.Fatal("inspection mutated journal")
	}
	body, _ := json.Marshal(after)
	if strings.Contains(string(body), "private-source") || strings.Contains(string(body), "observed answer") {
		t.Fatal("content escaped projection")
	}
}

func TestSkillTaskOutcomeHostReferenceAndMechanicalChecks(t *testing.T) {
	s, _ := generationStore(t)
	ctx := context.Background()
	use := &runtime.SkillContextUse{Version: 1, Complete: true, References: []runtime.SkillReference{{Scope: "project", Name: "skill", Version: strings.Repeat("a", 32), Digest: strings.Repeat("b", 64)}}}
	passed := true
	for i, kind := range []runtime.Kind{runtime.TaskStarted, runtime.TurnStarted, runtime.TurnCompleted, runtime.EvaluationRecorded, runtime.TaskCompleted} {
		e := event("outcome-"+string(kind), int64(i+1), kind)
		e.Data.Domain = "creative"
		e.Data.Profile = "default"
		if i == 0 {
			e.Data.SkillContext = use
			e.Data.Privacy = "local_only"
		}
		if i > 0 {
			e.TurnID = "turn"
			e.AttemptID = "attempt"
		}
		if kind == runtime.TurnStarted || kind == runtime.EvaluationRecorded {
			e.Data.ModelID = "model:tag"
			e.Data.ProviderID = "provider"
		}
		if kind == runtime.TurnCompleted {
			e.Data.Text = "text"
			e.Data.FinishReason = "stop"
		}
		if kind == runtime.EvaluationRecorded {
			e.Data.Code = "deterministic.nonempty_text.v1"
			e.Data.Accepted = &passed
		}
		if err := s.Append(ctx, int64(i), e); err != nil {
			t.Fatal(err)
		}
	}
	out, err := s.SkillTaskOutcome(ctx, "task")
	if err != nil || out.Quality != nil || len(out.OutputChecks) != 1 || !out.OutputChecks[0].Passed || !reflect.DeepEqual(out.SkillContext, use) {
		t.Fatal(out, err)
	}
	out.SkillContext.References[0].Name = "mutated"
	again, err := s.SkillTaskOutcome(ctx, "task")
	if err != nil || again.SkillContext.References[0].Name != "skill" {
		t.Fatal(again, err)
	}
}

func TestSkillTaskOutcomeNoAttemptAndGuards(t *testing.T) {
	for _, complete := range []bool{false, true} {
		s, _ := generationStore(t)
		ctx := context.Background()
		start := event("start", 1, runtime.TaskStarted)
		start.Data.ParentTaskID = "parent"
		start.Data.RetryOfTaskID = "retry"
		start.Data.SkillContext = &runtime.SkillContextUse{Version: 1, Complete: complete, References: []runtime.SkillReference{}}
		if err := s.Append(ctx, 0, start); err != nil {
			t.Fatal(err)
		}
		out, err := s.SkillTaskOutcome(ctx, "task")
		if err != nil || out.State != "running" || out.Key != nil || out.Quality != nil || out.SkillContext.Complete != complete || out.ParentTaskID != "parent" || out.RetryOfTaskID != "retry" {
			t.Fatal(out, err)
		}
		if err = s.Append(ctx, 1, event("cancel", 2, runtime.TaskCanceled)); err != nil {
			t.Fatal(err)
		}
		out, err = s.SkillTaskOutcome(ctx, "task")
		if err != nil || out.State != "canceled" || out.AttemptID != "" {
			t.Fatal(out, err)
		}
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		if out, err = s.SkillTaskOutcome(canceled, "task"); !errors.Is(err, context.Canceled) || out.Version != 0 {
			t.Fatal(out, err)
		}
		if _, err = s.SkillTaskOutcome(nil, "task"); err == nil {
			t.Fatal("nil context")
		}
		if _, err = s.SkillTaskOutcome(ctx, "missing"); err == nil {
			t.Fatal("missing task")
		}
	}
}

func TestSkillTaskOutcomeRejectsCorruptEvaluationHead(t *testing.T) {
	s, _ := generationStore(t)
	ctx := context.Background()
	r := workflowSourceFixture(t, s, "task-a", "session", "creative", runtime.TaskCompleted, evaluation.UserFeedback, true, true)
	if _, err := s.db.Exec("DELETE FROM evaluation_heads WHERE base_id=?", r.ID); err != nil {
		t.Fatal(err)
	}
	if out, err := s.SkillTaskOutcome(ctx, r.TaskID); err == nil || out.Version != 0 {
		t.Fatal("corruption became unknown", out, err)
	}
}
