package app

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

func groupedAppFixture(t *testing.T) (*Service, []string, *atomic.Int32) {
	t.Helper()
	fixture, cfg := autoFixture(t)
	var calls, streams atomic.Int32
	generations := new(atomic.Int32)
	factory := applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		return rotationGenerationProvider(func(ctx context.Context, request providers.Request, emit func(providers.Chunk) error) error {
			if len(request.Tools) == 0 && strings.Contains(request.Messages[0].Content, "Generalize") {
				generations.Add(1)
				return emit(providers.Chunk{Text: `{"version":1,"description":"Inspect with lookup","tags":[],"steps":["Look up the input","Check the result"],"required_tools":["lookup"],"configuration":"","risks":[],"validation_cases":["Validate the lookup result"]}`, Done: true, FinishReason: "stop"})
			}
			return (extensionProvider{t: t, wantTools: []string{"lookup"}, streams: &streams}).Stream(ctx, request, emit)
		}), nil
	})
	svc, err := NewServiceWithToolExtension(cfg, nil, nil, nil, nil, factory, applicationExtension(t, &calls))
	if err != nil {
		t.Fatal(err)
	}
	svc.profile = fixture.profile
	var tasks []string
	for range 2 {
		result, err := svc.Run(context.Background(), Request{ModelID: "a", Prompt: "Inspect with lookup", Domain: "code"})
		if err != nil {
			t.Fatal(err)
		}
		if err := RecordFeedback(context.Background(), cfg.Telemetry.Database, result.TaskID, true, 0); err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, result.TaskID)
	}
	if calls.Load() != 2 {
		t.Fatal("fixture did not execute tools", calls.Load())
	}
	svc.settings.Skills.Root = filepath.Join(t.TempDir(), "unpublished")
	svc.settings.Skills.Enabled, svc.settings.Skills.AutoDraft, svc.settings.Skills.Scope = true, true, "project"
	return svc, tasks, generations
}

func TestGroupedWorkflowSelectionActualExecutionToDraft(t *testing.T) {
	svc, tasks, calls := groupedAppFixture(t)
	ctx := context.Background()
	groups, err := svc.GroupSkillWorkflows(ctx, tasks)
	if err != nil || len(groups) != 1 || groups[0].Validate() != nil || !reflect.DeepEqual(groups[0].Tools, []string{"lookup"}) || calls.Load() != 0 {
		t.Fatal(groups, err, calls.Load())
	}
	key := skills.Key{Scope: "project", Name: "lookup-workflow"}
	selection, err := svc.PlanGroupedWorkflowSelection(ctx, "a", key, tasks, 0)
	if err != nil || selection.Group != groups[0].ID || selection.Algorithm != skills.ObservedToolsAlgorithm || !reflect.DeepEqual(selection.Sources, groups[0].Sources) || calls.Load() != 0 {
		t.Fatal(selection, err, calls.Load())
	}
	repeated, err := svc.PlanGroupedWorkflowSelection(ctx, "a", key, []string{tasks[1], tasks[0]}, 0)
	if err != nil || !reflect.DeepEqual(selection, repeated) {
		t.Fatal("unstable planning", repeated, err)
	}
	if _, err := svc.PlanWorkflowSelection(ctx, "a", key, selection.Group, skills.ObservedToolsAlgorithm, tasks, 0); err == nil {
		t.Fatal("unverified host grouping claimed reserved algorithm")
	}
	attempt, err := svc.GenerateSkillSelection(ctx, selection.ID, 0)
	if err != nil || attempt.Status != "drafted" || attempt.ID != selection.ID || calls.Load() != 1 {
		t.Fatal(attempt, err, calls.Load())
	}
	if _, err := svc.GenerateSkillSelection(ctx, selection.ID, 0); err == nil || calls.Load() != 1 {
		t.Fatal("repeat dispatched", err)
	}
	if _, err := os.Stat(svc.settings.Skills.Root); !os.IsNotExist(err) {
		t.Fatal("grouping published", err)
	}
}

func TestWorkflowGroupingSkipsNonProceduresAndRejectsPartialPlan(t *testing.T) {
	svc, tasks := skillGenerationAppFixture(t)
	groups, err := svc.GroupSkillWorkflows(context.Background(), tasks)
	if err != nil || groups == nil || len(groups) != 0 {
		t.Fatal("text-only tasks grouped", groups, err)
	}
	if _, err := svc.PlanGroupedWorkflowSelection(context.Background(), "a", skills.Key{Scope: "project", Name: "workflow"}, tasks, 0); err == nil {
		t.Fatal("text-only plan admitted")
	}
	svc, tasks, _ = groupedAppFixture(t)
	history, err := FeedbackHistory(context.Background(), svc.settings.Telemetry.Database, tasks[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := ReviseFeedback(context.Background(), svc.settings.Telemetry.Database, tasks[0], history[0].ID, false); err != nil {
		t.Fatal(err)
	}
	groups, err = svc.GroupSkillWorkflows(context.Background(), tasks)
	if err != nil || len(groups) != 0 {
		t.Fatal("rejected example grouped", groups, err)
	}
	if _, err := svc.PlanGroupedWorkflowSelection(context.Background(), "a", skills.Key{Scope: "project", Name: "workflow"}, tasks, 0); err == nil {
		t.Fatal("partial plan admitted")
	}
}

func TestWorkflowGroupBindingRejectsChangedEvidenceBeforeSave(t *testing.T) {
	svc, tasks, calls := groupedAppFixture(t)
	ctx := context.Background()
	groups, err := svc.GroupSkillWorkflows(ctx, tasks)
	if err != nil || len(groups) != 1 {
		t.Fatal(groups, err)
	}
	group := groups[0]
	history, err := FeedbackHistory(ctx, svc.settings.Telemetry.Database, tasks[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := ReviseFeedback(ctx, svc.settings.Telemetry.Database, tasks[0], history[0].ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.planWorkflowSelection(ctx, "a", skills.Key{Scope: "project", Name: "workflow"}, group.ID, group.Algorithm, tasks, 0, &group, nil); err == nil || calls.Load() != 0 {
		t.Fatal("stale grouping saved", err, calls.Load())
	}
	db, err := telemetry.OpenReadOnly(ctx, svc.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	selections, err := db.ListWorkflowSelections(ctx, "project", "", 100)
	if err != nil || len(selections) != 0 {
		t.Fatal("stale group persisted", selections, err)
	}
}

func TestWorkflowGroupingSecretAndContextGuards(t *testing.T) {
	svc, tasks, _ := groupedAppFixture(t)
	svc.secret = func(name string) string {
		if name == "DARWIN_API_TOKEN" {
			return "lookup"
		}
		return ""
	}
	if groups, err := svc.GroupSkillWorkflows(context.Background(), tasks); err == nil || groups != nil {
		t.Fatal("tool identity credential leaked", groups, err)
	}
	for _, service := range []*Service{nil, svc} {
		if groups, err := service.GroupSkillWorkflows(nil, tasks); err == nil || groups != nil {
			t.Fatal(groups, err)
		}
		if _, err := service.PlanGroupedWorkflowSelection(nil, "a", skills.Key{Scope: "project", Name: "workflow"}, tasks, 0); err == nil {
			t.Fatal("nil context admitted")
		}
	}
}

func TestGroupedGenerationRechecksChangedToolOutcome(t *testing.T) {
	svc, tasks, calls := groupedAppFixture(t)
	ctx := context.Background()
	selection, err := svc.PlanGroupedWorkflowSelection(ctx, "a", skills.Key{Scope: "project", Name: "workflow"}, tasks, 0)
	if err != nil {
		t.Fatal(err)
	}
	changed := rewriteCanonicalAppEventsForTest(t, svc.settings.Telemetry.Database, tasks[0], func(event runtime.Event) bool {
		return event.Kind == runtime.ToolCompleted
	}, func(event *runtime.Event) { event.Data.Code = "tool_failed" })
	if changed != 1 {
		t.Fatal("fixture did not alter completion", changed)
	}
	db, err := telemetry.OpenReadOnly(ctx, svc.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sources, err := db.SkillWorkflowSources(ctx, tasks)
	if err != nil || reflect.DeepEqual(workflowSelectionCandidates(sources), selection.Sources) {
		t.Fatal("changed tool failure did not change candidate digests", err)
	}
	if _, err := svc.GenerateSkillSelection(ctx, selection.ID, 0); err == nil || calls.Load() != 0 {
		t.Fatal("changed execution dispatched despite group check", err, calls.Load())
	}
	attempts, err := db.ListSkillGenerationAttempts(ctx, "project", "", 100)
	if err != nil || len(attempts) != 0 {
		t.Fatal("bad execution claimed attempt", attempts, err)
	}
}
