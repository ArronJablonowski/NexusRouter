package telemetry

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/skills"
)

// Positive task-level feedback is independent of the actual tool outcome. A
// historical completed task can still contain a failed create operation (for
// example, history recorded by an older runtime); it must never teach creation
// success merely because the user liked the final answer.
func createProcedureFixture(t *testing.T, s *Store, task string, failed bool) {
	t.Helper()
	code := ""
	if failed {
		code = "tool_failed"
	}
	procedureFixture(t, s, task, code, runtime.NoEffect)
	events, err := s.Read(context.Background(), task, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, index := range []int{2, 5, 6} {
		e := events[index]
		if index == 2 {
			e.Data.ToolCalls[1].Name = "create_file"
			e.Data.ToolCalls[1].Arguments = json.RawMessage(`{"path":"result.txt","content":"new file"}`)
		} else {
			e.Data.ToolName = "create_file"
			if index == 6 {
				if failed {
					e.Data.Text = `{"error":"file_create_unavailable"}`
					e.Data.Effect = runtime.NoEffect
				} else {
					e.Data.Text = `{"created":true}`
					e.Data.Effect = runtime.ConfirmedEffect
				}
			}
		}
		rewriteCanonicalEventForTest(t, s, task, e.Sequence, func(event *runtime.Event) { *event = e })
	}
}

func TestWorkflowCreateFailureCannotBecomeSuccessfulProcedure(t *testing.T) {
	s, _ := generationStore(t)
	ctx := context.Background()
	createProcedureFixture(t, s, "create-a", false)
	createProcedureFixture(t, s, "create-b", false)
	createProcedureFixture(t, s, "create-failed", true)
	ids := []string{"create-a", "create-b", "create-failed"}
	before := workflowSourceRawBodies(t, s)
	// All three have separate positive user feedback and completed task state.
	// Rejection must therefore come from the observed create_file failure code.
	sources, err := s.SkillWorkflowSources(ctx, ids)
	if err != nil || len(sources) != 3 {
		t.Fatal("positive feedback fixture missing", sources, err)
	}
	procedures, err := s.SkillWorkflowProcedures(ctx, ids)
	if err != nil || len(procedures) != 2 {
		t.Fatal("failed creation entered procedures", procedures, err)
	}
	for _, p := range procedures {
		if p.Candidate.TaskID == "create-failed" || !reflect.DeepEqual(p.Tools, []string{"read_file", "create_file", "read_file"}) {
			t.Fatal("incorrect observed tool trajectory", p)
		}
	}
	groups, err := skills.BuildWorkflowGroups(procedures)
	if err != nil || len(groups) != 1 {
		t.Fatal(groups, err)
	}
	if got, err := s.SkillWorkflowGroupSources(ctx, []string{"create-a", "create-b"}, groups[0].ID); err != nil || len(got) != 2 {
		t.Fatal("confirmed creation control rejected", got, err)
	}
	if got, err := s.SkillWorkflowGroupSources(ctx, []string{"create-a", "create-failed"}, groups[0].ID); err == nil || got != nil {
		t.Fatal("failed create grouped using unrelated positive feedback", got, err)
	}
	if !reflect.DeepEqual(before, workflowSourceRawBodies(t, s)) {
		t.Fatal("inspection rewrote failure or feedback evidence")
	}
}
