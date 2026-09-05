package telemetry

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func TestWorkflowDiscoveryScansIneligibleAndMatchesSources(t *testing.T) {
	s, path := generationStore(t)
	ctx := context.Background()
	workflowSourceFixture(t, s, "task-a", "session-a", "creative", runtime.TaskFailed, evaluation.UserFeedback, true, true)
	workflowSourceFixture(t, s, "task-b", "session-b", "creative", runtime.TaskCompleted, evaluation.LLMJudge, true, true)
	a := workflowSourceFixture(t, s, "task-c", "session-c", "creative", runtime.TaskCompleted, evaluation.Deterministic, true, true)
	b := workflowSourceFixture(t, s, "task-d", "session-d", "creative", runtime.TaskCompleted, evaluation.UserFeedback, true, true)
	workflowSourceFixture(t, s, "task-e", "session-e", "code", runtime.TaskCompleted, evaluation.Deterministic, true, true)
	before := workflowSourceRawBodies(t, s)
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	first, err := ro.DiscoverSkillWorkflows(ctx, "creative", "", 2)
	if err != nil || first.Version != 1 || first.Domain != "creative" || first.Scanned != 2 || first.Next != "task-b" || first.Candidates == nil || len(first.Candidates) != 0 {
		t.Fatal(first, err)
	}
	second, err := ro.DiscoverSkillWorkflows(ctx, "creative", first.Next, 2)
	if err != nil || second.Scanned != 2 || second.Next != "task-d" || len(second.Candidates) != 2 {
		t.Fatal(second, err)
	}
	sources, err := ro.SkillWorkflowSources(ctx, []string{a.TaskID, b.TaskID})
	if err != nil {
		t.Fatal(err)
	}
	for i, c := range second.Candidates {
		source := sources[i]
		if c.TaskID != source.Example.TaskID || c.SessionID != source.Example.SessionID || c.Domain != source.Example.Domain || c.Privacy != source.Privacy || c.EvaluationID != source.EvaluationID || c.EvaluationDigest != source.EvaluationDigest || c.SourceDigest != source.SourceDigest || c.SourceSequence != source.SourceSequence {
			t.Fatal("discovery provenance mismatch", c, source)
		}
	}
	encoded, _ := json.Marshal(second)
	for _, private := range []string{"private-source-request", "private-system-instruction", "observed answer", "steps", "checks"} {
		if strings.Contains(string(encoded), private) {
			t.Fatal("metadata discovery leaked workflow")
		}
	}
	last, err := ro.DiscoverSkillWorkflows(ctx, "creative", second.Next, 2)
	if err != nil || last.Scanned != 1 || last.Next != "" || len(last.Candidates) != 0 {
		t.Fatal(last, err)
	}
	if !reflect.DeepEqual(before, workflowSourceRawBodies(t, s)) {
		t.Fatal("discovery mutated durable records")
	}
	restarted, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	again, err := restarted.DiscoverSkillWorkflows(ctx, "creative", first.Next, 2)
	if err != nil || !reflect.DeepEqual(second, again) {
		t.Fatal("restart changed discovery", again, err)
	}
	rejected := b
	rejected.ID = "negative-revision"
	rejected.Checks = []evaluation.Check{{Source: evaluation.UserFeedback, Reference: "negative-feedback", Passed: false}}
	if err := s.SupersedeEvaluation(ctx, b.ID, rejected); err != nil {
		t.Fatal(err)
	}
	current, err := ro.DiscoverSkillWorkflows(ctx, "creative", first.Next, 2)
	if err != nil || len(current.Candidates) != 1 || current.Candidates[0].TaskID != a.TaskID || current.Next != "task-d" {
		t.Fatal("older acceptance survived rejection", current, err)
	}
}

func TestWorkflowDiscoveryInvalidParametersAndCancellation(t *testing.T) {
	s, _ := generationStore(t)
	for _, test := range []struct {
		domain, after string
		limit         int
	}{{"", "", 2}, {"../private", "", 2}, {strings.Repeat("a", 65), "", 2}, {"creative", strings.Repeat("a", 129), 2}, {"creative", "", 0}, {"creative", "", 21}} {
		page, err := s.DiscoverSkillWorkflows(context.Background(), test.domain, test.after, test.limit)
		if err == nil || len(page.Candidates) != 0 || strings.Contains(err.Error(), "../private") {
			t.Fatal("unsafe invalid query", page, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if page, err := s.DiscoverSkillWorkflows(ctx, "creative", "", 2); err == nil || len(page.Candidates) != 0 {
		t.Fatal("canceled discovery succeeded", page, err)
	}
	if page, err := s.DiscoverSkillWorkflows(nil, "creative", "", 2); err == nil || len(page.Candidates) != 0 {
		t.Fatal("nil context succeeded", page, err)
	}
}

func TestWorkflowDiscoverySkipsMissingEvidenceAndCanceledTasks(t *testing.T) {
	s, _ := generationStore(t)
	ctx := context.Background()
	workflowSourceFixture(t, s, "task-a", "session-a", "creative", runtime.TaskCompleted, evaluation.UserFeedback, true, false)
	workflowSourceFixture(t, s, "task-b", "session-b", "creative", runtime.TaskCanceled, evaluation.UserFeedback, true, true)
	page, err := s.DiscoverSkillWorkflows(ctx, "creative", "", 20)
	if err != nil || page.Scanned != 2 || page.Next != "" || page.Candidates == nil || len(page.Candidates) != 0 {
		t.Fatal(page, err)
	}
}

func TestWorkflowDiscoveryCorruptionReturnsNoPartialCandidates(t *testing.T) {
	for _, mode := range []string{"event", "evaluation"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := generationStore(t)
			ctx := context.Background()
			workflowSourceFixture(t, s, "task-a", "session-a", "creative", runtime.TaskCompleted, evaluation.Deterministic, true, true)
			workflowSourceFixture(t, s, "task-b", "session-b", "creative", runtime.TaskCompleted, evaluation.UserFeedback, true, true)
			query := `UPDATE events SET body='{"private":"private-corrupt-body"}' WHERE task_id='task-b' AND sequence=3`
			if mode == "evaluation" {
				query = `UPDATE evaluations SET body='private-corrupt-body' WHERE task_id='task-b'`
			}
			if _, err := s.db.Exec(query); err != nil {
				t.Fatal(err)
			}
			before := workflowSourceRawBodies(t, s)
			page, err := s.DiscoverSkillWorkflows(ctx, "creative", "", 2)
			if err == nil || len(page.Candidates) != 0 || strings.Contains(err.Error(), "private-corrupt-body") {
				t.Fatal("corrupt discovery returned partial result", page, err)
			}
			if !reflect.DeepEqual(before, workflowSourceRawBodies(t, s)) {
				t.Fatal("discovery repaired corruption")
			}
		})
	}
}
