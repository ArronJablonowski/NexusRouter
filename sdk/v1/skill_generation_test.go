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
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	sdk "github.com/ArronJablonowski/DarwinRouter/sdk/v1"
	"github.com/ArronJablonowski/DarwinRouter/skills"
	"go.yaml.in/yaml/v3"
)

func TestSDKGenerateSkillDraftFromCompletedFeedback(t *testing.T) {
	options, database := sdkToolOptions(t)
	var calls, generations atomic.Int32
	options.ProviderFactory = sdkProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		return sdkProviderStream(func(_ context.Context, request providers.Request, emit func(providers.Chunk) error) error {
			calls.Add(1)
			if len(request.Messages) > 0 && strings.Contains(request.Messages[0].Content, "Generalize") {
				generations.Add(1)
				if len(request.Tools) != 0 || request.Model != "fixture" || len(request.Messages) != 2 || !strings.Contains(request.Messages[1].Content, "Inspect the requirement") {
					t.Error("incorrect auxiliary request")
				}
				return emit(providers.Chunk{Text: `{"version":1,"description":"Reusable writing workflow","tags":["creative"],"steps":["Inspect the requirement","Draft and check the response"],"required_tools":[],"configuration":"","risks":["Respect subjective taste"],"validation_cases":["Check explicit constraints"]}`, Done: true, FinishReason: "stop", Usage: &providers.Usage{InputTokens: 20, OutputTokens: 30}})
			}
			return emit(providers.Chunk{Text: "Inspect the requirement and produce the requested story.", Done: true, FinishReason: "stop"})
		}), nil
	})
	client, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var tasks []string
	for range 2 {
		result, err := client.Run(ctx, sdk.Request{Version: 1, ModelID: "chat", Prompt: "Write a short story", Domain: "creative"})
		if err != nil {
			t.Fatal(err)
		}
		if err = client.Feedback(ctx, result.TaskID, true, 0); err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, result.TaskID)
	}
	// Opt in to proposal generation separately; no skill catalog is published.
	body, err := os.ReadFile(options.ProjectFile)
	if err != nil {
		t.Fatal(err)
	}
	var cfg config.Settings
	if err = yaml.Unmarshal(body, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Skills.Enabled, cfg.Skills.AutoDraft = true, true
	cfg.Skills.Root = filepath.Join(t.TempDir(), "unpublished")
	cfg.Skills.Scope = "project"
	body, err = yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(options.ProjectFile, body, 0600); err != nil {
		t.Fatal(err)
	}
	client, err = sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	key := skills.Key{Scope: "project", Name: "writing"}
	attempt, err := client.GenerateSkillDraft(ctx, "sdk-generation", "chat", key, tasks, 0)
	if err != nil || attempt.Status != "drafted" || attempt.Result == nil || generations.Load() != 1 || calls.Load() != 3 {
		t.Fatalf("generation failed: %+v %v calls=%d", attempt, err, calls.Load())
	}
	if attempt.Result.Draft.Key != key || len(attempt.Result.Draft.SourceSessions) != 2 || len(attempt.Result.Draft.SourceEvidence) != 2 || attempt.Result.Usage == nil || attempt.Result.Usage.InputTokens != 20 {
		t.Fatalf("missing canonical metadata/accounting: %+v", attempt.Result)
	}
	ro, err := telemetry.OpenReadOnly(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	saved, err := ro.SkillGenerationAttempt(ctx, attempt.ID)
	if err != nil || !reflect.DeepEqual(saved, attempt) {
		t.Fatalf("returned uncommitted proposal: %+v %v", saved, err)
	}
	if _, err = client.GenerateSkillDraft(ctx, "sdk-generation", "chat", key, tasks, 0); err == nil || generations.Load() != 1 {
		t.Fatal("duplicate generation dispatched", err)
	}
	if _, err = os.Stat(cfg.Skills.Root); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("proposal published a skill store", err)
	}
}

func TestSDKGenerateSkillDraftClientAndCancellationGuards(t *testing.T) {
	key := skills.Key{Scope: "project", Name: "workflow"}
	for _, client := range []*sdk.Client{nil, {}} {
		got, err := client.GenerateSkillDraft(context.Background(), "attempt", "chat", key, []string{"one", "two"}, 0)
		if !errors.Is(err, sdk.ErrAdmission) || !reflect.DeepEqual(got, skills.GenerationAttempt{}) {
			t.Fatalf("invalid client accepted: %+v %v", got, err)
		}
	}
	options, database := sdkToolOptions(t)
	var calls atomic.Int32
	options.Overrides = map[string]string{"skills.enabled": "true", "skills.auto_draft": "true", "skills.root": filepath.Join(t.TempDir(), "unpublished"), "skills.scope": "project"}
	options.ProviderFactory = sdkProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		calls.Add(1)
		return nil, errors.New("must not execute")
	})
	client, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, invalidCtx := range []context.Context{nil, ctx} {
		got, err := client.GenerateSkillDraft(invalidCtx, "attempt", "chat", key, []string{"one", "two"}, 0)
		if err == nil || calls.Load() != 0 || !reflect.DeepEqual(got, skills.GenerationAttempt{}) {
			t.Fatalf("invalid context dispatched: %+v %v", got, err)
		}
	}
	if _, err = os.Stat(database); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid context created storage", err)
	}
}
