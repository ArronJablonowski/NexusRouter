package v1_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	sdk "github.com/ArronJablonowski/DarwinRouter/sdk/v1"
	"github.com/ArronJablonowski/DarwinRouter/skills"
	"github.com/ArronJablonowski/DarwinRouter/tools"
)

func TestSDKWorkflowGroupActualTools(t *testing.T) {
	options, _ := sdkToolOptions(t)
	var calls, executions, generations atomic.Int32
	options.Tools = []sdk.Tool{sdkReadTool(&executions)}
	options.ToolPolicy = &sdk.ToolPolicy{Default: tools.Allow}
	options.ProviderFactory = sdkProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		return sdkProviderStream(func(_ context.Context, request providers.Request, emit func(providers.Chunk) error) error {
			calls.Add(1)
			if strings.Contains(request.Messages[0].Content, "Generalize") {
				generations.Add(1)
				return emit(providers.Chunk{Text: `{"version":1,"description":"Look up a public fact","tags":["general"],"steps":["Look up the fact","Check the answer"],"required_tools":["lookup_fact"],"configuration":"","risks":["Check provenance"],"validation_cases":["Verify expected fact"]}`, Done: true, FinishReason: "stop"})
			}
			last := request.Messages[len(request.Messages)-1]
			if last.Role != "tool" && !strings.Contains(last.Content, "without tools") {
				if err := emit(providers.Chunk{ToolCall: &providers.ToolCall{ID: "lookup", Name: "lookup_fact", Arguments: json.RawMessage(`{"key":"answer"}`)}}); err != nil {
					return err
				}
				return emit(providers.Chunk{Done: true, FinishReason: "tool_calls"})
			}
			return emit(providers.Chunk{Text: "The answer is forty-two.", Done: true, FinishReason: "stop"})
		}), nil
	})
	client, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	run := func(prompt string) string {
		t.Helper()
		result, err := client.Run(ctx, sdk.Request{Version: 1, ModelID: "chat", Prompt: prompt, Domain: "general"})
		if err != nil {
			t.Fatal(err)
		}
		if err := client.Feedback(ctx, result.TaskID, true, 0); err != nil {
			t.Fatal(err)
		}
		return result.TaskID
	}
	tasks := []string{run("Look up a fact"), run("Look up a fact again")}
	noTools := run("Answer without tools")
	if executions.Load() != 2 {
		t.Fatal("expected actual tool execution")
	}
	root := filepath.Join(t.TempDir(), "unpublished")
	options.Overrides = map[string]string{"skills.enabled": "true", "skills.auto_draft": "true", "skills.root": root, "skills.scope": "project"}
	client, err = sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	before := calls.Load()
	groups, err := client.GroupSkillWorkflows(ctx, tasks)
	if err != nil || len(groups) != 1 || groups[0].Validate() != nil || !reflect.DeepEqual(groups[0].Tools, []string{"lookup_fact"}) {
		t.Fatalf("groups: %+v %v", groups, err)
	}
	for _, ids := range [][]string{{noTools}, {tasks[0]}, {tasks[0], noTools}} {
		groups, err := client.GroupSkillWorkflows(ctx, ids)
		if err != nil || groups == nil || len(groups) != 0 {
			t.Fatalf("singleton/no-tool groups: %+v %v", groups, err)
		}
	}
	key := skills.Key{Scope: "project", Name: "lookup"}
	selection, err := client.PlanGroupedWorkflowSelection(ctx, "chat", key, tasks, 0)
	if err != nil || selection.Validate() != nil || selection.Algorithm != skills.ObservedToolsAlgorithm || calls.Load() != before {
		t.Fatalf("plan: %+v %v", selection, err)
	}
	repeat, err := client.PlanGroupedWorkflowSelection(ctx, "chat", key, []string{tasks[1], tasks[0]}, 0)
	if err != nil || !reflect.DeepEqual(repeat, selection) {
		t.Fatal("unstable grouping selection", err)
	}
	if _, err := client.PlanWorkflowSelection(ctx, "chat", key, selection.Group, skills.ObservedToolsAlgorithm, tasks, 0); err == nil {
		t.Fatal("caller asserted reserved algorithm")
	}
	if _, err := client.PlanGroupedWorkflowSelection(ctx, "chat", key, []string{tasks[0], noTools}, 0); err == nil {
		t.Fatal("planner silently omitted source")
	}
	attempt, err := client.GenerateSkillSelection(ctx, selection.ID, 0)
	if err != nil || attempt.ID != selection.ID || attempt.Status != "drafted" || generations.Load() != 1 {
		t.Fatalf("generate: %+v %v", attempt, err)
	}
	if _, err := client.GenerateSkillSelection(ctx, selection.ID, 0); err == nil || generations.Load() != 1 {
		t.Fatal("group redispatched", err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("group activated/published skill", err)
	}
}

func TestSDKWorkflowGroupGuards(t *testing.T) {
	key := skills.Key{Scope: "project", Name: "lookup"}
	for _, client := range []*sdk.Client{nil, {}} {
		if groups, err := client.GroupSkillWorkflows(context.Background(), []string{"one"}); !errors.Is(err, sdk.ErrAdmission) || groups != nil {
			t.Fatal("invalid client grouped", err)
		}
		if selection, err := client.PlanGroupedWorkflowSelection(context.Background(), "chat", key, []string{"one", "two"}, 0); !errors.Is(err, sdk.ErrAdmission) || !reflect.DeepEqual(selection, skills.WorkflowSelection{}) {
			t.Fatal("invalid client planned", err)
		}
	}
	options, database := sdkToolOptions(t)
	options.Overrides = map[string]string{"skills.enabled": "true", "skills.auto_draft": "true", "skills.root": filepath.Join(t.TempDir(), "unpublished"), "skills.scope": "project"}
	client, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, invalid := range []context.Context{nil, ctx} {
		if groups, err := client.GroupSkillWorkflows(invalid, []string{"one"}); err == nil || groups != nil {
			t.Fatal("invalid context grouped", err)
		}
		if selection, err := client.PlanGroupedWorkflowSelection(invalid, "chat", key, []string{"one", "two"}, 0); err == nil || !reflect.DeepEqual(selection, skills.WorkflowSelection{}) {
			t.Fatal("invalid context planned", err)
		}
	}
	if _, err := os.Stat(database); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid context created storage", err)
	}
}
