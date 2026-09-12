package telemetry

import (
	"context"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func TestWorkflowDiscoverySkipsUnsupportedValidLabels(t *testing.T) {
	for _, kind := range []string{"domain-long", "domain-dot", "profile-long", "profile-dot"} {
		t.Run(kind, func(t *testing.T) {
			s, _ := generationStore(t)
			label := strings.Repeat("a", 65)
			if strings.HasSuffix(kind, "dot") {
				label = "valid.label"
			}
			domain := "creative"
			if strings.HasPrefix(kind, "domain") {
				domain = label
			}
			workflowSourceFixture(t, s, "task-a", "session-a", domain, runtime.TaskCompleted, evaluation.Deterministic, true, false)
			if strings.HasPrefix(kind, "profile") {
				events, err := s.Read(context.Background(), "task-a", 0, 100)
				if err != nil {
					t.Fatal(err)
				}
				e := events[0]
				e.Data.Profile = label
				rewriteCanonicalEventForTest(t, s, "task-a", 1, func(event *runtime.Event) { *event = e })
			}
			workflowSourceFixture(t, s, "task-b", "session-b", "creative", runtime.TaskCompleted, evaluation.Deterministic, true, true)
			page, err := s.DiscoverSkillWorkflows(context.Background(), "creative", "", 2)
			if err != nil || page.Scanned != 2 || page.Next != "task-b" || len(page.Candidates) != 1 || page.Candidates[0].TaskID != "task-b" {
				t.Fatal("unsupported label blocked discovery", page, err)
			}
			if _, err = s.SkillWorkflowSources(context.Background(), []string{"task-a", "task-b"}); err == nil {
				t.Fatal("explicit selection accepted unsupported label")
			}
		})
	}
}

func TestWorkflowDiscoverySkipsLargeValidSource(t *testing.T) {
	s, _ := generationStore(t)
	ctx := context.Background()
	workflowSourceFixture(t, s, "task-a", "session-a", "creative", runtime.TaskCompleted, evaluation.Deterministic, true, true)
	workflowSourceFixture(t, s, "task-b", "session-b", "creative", runtime.TaskCompleted, evaluation.Deterministic, true, true)
	events, err := s.Read(ctx, "task-a", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	e := events[0]
	e.Data.Messages[1].Content = strings.Repeat("x", 257<<10)
	rewriteCanonicalEventForTest(t, s, "task-a", 1, func(event *runtime.Event) { *event = e })
	if _, err = s.TaskSnapshot(ctx, "task-a"); err != nil {
		t.Fatal("large fixture is invalid", err)
	}
	page, err := s.DiscoverSkillWorkflows(ctx, "creative", "", 2)
	if err != nil || page.Scanned != 2 || page.Next != "task-b" || len(page.Candidates) != 1 || page.Candidates[0].TaskID != "task-b" {
		t.Fatal("large valid source blocked discovery", page, err)
	}
	if _, err = s.SkillWorkflowSources(ctx, []string{"task-a", "task-b"}); err == nil {
		t.Fatal("explicit source budget bypassed")
	}
}
