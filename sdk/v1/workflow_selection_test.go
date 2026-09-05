package v1_test

import (
	"context"
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
)

func TestSDKWorkflowSelectionStableGeneration(t *testing.T) {
	options, _ := sdkToolOptions(t)
	root := filepath.Join(t.TempDir(), "unpublished")
	var calls, generations atomic.Int32
	options.ProviderFactory = sdkProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		return sdkProviderStream(func(_ context.Context, request providers.Request, emit func(providers.Chunk) error) error {
			calls.Add(1)
			if len(request.Messages) > 0 && strings.Contains(request.Messages[0].Content, "Generalize") {
				generations.Add(1)
				return emit(providers.Chunk{Text: `{"version":1,"description":"Reusable writing workflow","tags":["creative"],"steps":["Read requirements","Draft and check the response"],"required_tools":[],"configuration":"","risks":["Respect subjective taste"],"validation_cases":["Check explicit constraints"]}`, Done: true, FinishReason: "stop"})
			}
			return emit(providers.Chunk{Text: "Read requirements and write a short story.", Done: true, FinishReason: "stop"})
		}), nil
	})
	client, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var tasks []string
	for range 2 {
		result, err := client.Run(ctx, sdk.Request{Version: 1, ModelID: "chat", Prompt: "Write a short story", Domain: "creative"})
		if err != nil {
			t.Fatal(err)
		}
		if err := client.Feedback(ctx, result.TaskID, true, 0); err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, result.TaskID)
	}
	key := skills.Key{Scope: "project", Name: "writing"}
	options.Overrides = map[string]string{"skills.enabled": "true", "skills.auto_draft": "true", "skills.root": root, "skills.scope": "project"}
	client, err = sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	selection, err := client.PlanWorkflowSelection(ctx, "chat", key, "writing", "fixture_v1", tasks, 0)
	if err != nil || selection.Validate() != nil || calls.Load() != 2 {
		t.Fatalf("planning: %+v %v calls=%d", selection, err, calls.Load())
	}
	client, err = sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := client.PlanWorkflowSelection(ctx, "chat", key, "writing", "fixture_v1", []string{tasks[1], tasks[0]}, 0)
	if err != nil || !reflect.DeepEqual(repeated, selection) || calls.Load() != 2 {
		t.Fatalf("planning retry: %+v %v", repeated, err)
	}
	attempt, err := client.GenerateSkillSelection(ctx, selection.ID, 0)
	if err != nil || attempt.ID != selection.ID || attempt.Status != "drafted" || generations.Load() != 1 {
		t.Fatalf("generation: %+v %v", attempt, err)
	}
	client, err = sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.GenerateSkillSelection(ctx, selection.ID, 0); err == nil || generations.Load() != 1 {
		t.Fatal("selection redispatched", err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("selection published a skill", err)
	}
}

func TestSDKWorkflowSelectionGuards(t *testing.T) {
	key := skills.Key{Scope: "project", Name: "writing"}
	for _, client := range []*sdk.Client{nil, {}} {
		selection, err := client.PlanWorkflowSelection(context.Background(), "chat", key, "writing", "fixture_v1", []string{"one", "two"}, 0)
		if !errors.Is(err, sdk.ErrAdmission) || !reflect.DeepEqual(selection, skills.WorkflowSelection{}) {
			t.Fatal("invalid client planned", err)
		}
		attempt, err := client.GenerateSkillSelection(context.Background(), strings.Repeat("a", 64), 0)
		if !errors.Is(err, sdk.ErrAdmission) || !reflect.DeepEqual(attempt, skills.GenerationAttempt{}) {
			t.Fatal("invalid client generated", err)
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
		selection, err := client.PlanWorkflowSelection(invalid, "chat", key, "writing", "fixture_v1", []string{"one", "two"}, 0)
		if err == nil || !reflect.DeepEqual(selection, skills.WorkflowSelection{}) {
			t.Fatal("invalid context planned", err)
		}
		attempt, err := client.GenerateSkillSelection(invalid, strings.Repeat("a", 64), 0)
		if err == nil || !reflect.DeepEqual(attempt, skills.GenerationAttempt{}) {
			t.Fatal("invalid context generated", err)
		}
	}
	if _, err := os.Stat(database); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid context created storage", err)
	}
}
