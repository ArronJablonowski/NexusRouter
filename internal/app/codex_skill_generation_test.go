package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/codexbridge"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

const codexSkillDraftJSON = `{"version":1,"description":"Creative workflow","tags":["creative"],"steps":["Inspect private-token requirements"],"required_tools":[],"configuration":"","risks":["Respect user taste"],"validation_cases":["Check explicit user constraints"]}`

func codexSkillGenerationFixture(t *testing.T) (*Service, []string, *telemetry.Store) {
	t.Helper()
	svc, first, db := codexAuditSource(t)
	second, err := svc.Run(context.Background(), Request{ModelID: "a", Prompt: "Another creative private-token task", Domain: "creative", Profile: "default"})
	if err != nil {
		t.Fatal(err)
	}
	tasks := []string{first, second.TaskID}
	for _, task := range tasks {
		if err := RecordFeedback(context.Background(), svc.settings.Telemetry.Database, task, true, 0); err != nil {
			t.Fatal(err)
		}
	}
	svc.settings.Skills.Enabled, svc.settings.Skills.AutoDraft = true, true
	svc.settings.Skills.LocalOnly = false
	svc.settings.Skills.Scope = "project"
	svc.settings.Skills.Root = filepath.Join(t.TempDir(), "unpublished-codex-skills")
	return svc, tasks, db
}

func TestCodexSkillGenerationLazyLaunchDraftAndCleanup(t *testing.T) {
	svc, tasks, db := codexSkillGenerationFixture(t)
	ctx := context.Background()
	before, err := db.SkillWorkflowSources(ctx, tasks)
	if err != nil {
		t.Fatal(err)
	}
	estimated := false
	svc.contextEstimator = auxiliaryContextEstimator(func(context.Context, providers.Request) (int, error) { estimated = true; return 1, nil })
	launches, streams := 0, 0
	directory := ""
	p := &codexAuditProviderFixture{}
	p.stream = func(_ context.Context, r providers.Request, emit func(providers.Chunk) error) error {
		streams++
		if r.Model != "gpt-5.6-sol" || len(r.Messages) != 2 || r.Messages[0].Role != "system" || r.Messages[1].Role != "user" || len(r.Tools) != 0 {
			t.Fatal("invalid generation envelope")
		}
		var schema map[string]any
		if json.Unmarshal(r.JSONSchema, &schema) != nil || schema["additionalProperties"] != false {
			t.Fatal("generation schema not closed")
		}
		if strings.Contains(r.Messages[1].Content, "private-token") || !strings.Contains(r.Messages[1].Content, "[REDACTED]") {
			t.Fatal("source secret leaked")
		}
		return emit(providers.Chunk{Text: codexSkillDraftJSON, Done: true, FinishReason: "stop", Usage: &providers.Usage{InputTokens: 7, OutputTokens: 13}})
	}
	svc.codexLauncher = func(_ context.Context, spec codexbridge.LaunchSpec) (taskProvider, error) {
		launches++
		directory = spec.CWD
		attempt, err := db.SkillGenerationAttempt(ctx, "codex-generation")
		if !estimated || spec.Model != "gpt-5.6-sol" || spec.Privacy != "cloud_allowed" || err != nil || attempt.Status != "started" {
			t.Fatal("launch before admission/durable start", attempt, err)
		}
		return p, nil
	}
	key := skills.Key{Scope: "project", Name: "codex-workflow"}
	a, err := svc.GenerateSkillDraft(ctx, "codex-generation", "brain", key, tasks, 0)
	if err != nil || a.Status != "drafted" || a.Result == nil || a.Result.Usage == nil || a.Result.Usage.InputTokens != 7 || a.Result.Usage.OutputTokens != 13 || launches != 1 || streams != 1 || p.closed != 1 {
		t.Fatal(a, err, launches, streams, p.closed)
	}
	if a.Result.Draft.Key != key || len(a.Result.Draft.SourceSessions) != 2 {
		t.Fatal("provenance not host controlled")
	}
	body, _ := json.Marshal(a)
	if strings.Contains(string(body), "private-token") || !strings.Contains(string(body), "[REDACTED]") {
		t.Fatal("draft output secret persisted")
	}
	saved, err := db.SkillGenerationAttempt(ctx, a.ID)
	if err != nil || !reflect.DeepEqual(saved, a) {
		t.Fatal("attempt not durable", err)
	}
	after, err := db.SkillWorkflowSources(ctx, tasks)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("generation changed sources", err)
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatal("owned Codex directory retained", err)
	}
	if _, err := os.Stat(svc.settings.Skills.Root); !os.IsNotExist(err) {
		t.Fatal("generated proposal published/activated", err)
	}
	if _, err := svc.GenerateSkillDraft(ctx, a.ID, "brain", key, tasks, 0); err == nil || launches != 1 {
		t.Fatal("duplicate launched", err)
	}
}

func TestCodexSkillGenerationFailuresNeverRedispatch(t *testing.T) {
	for _, mode := range []string{"stream", "malformed", "tool", "cancel", "panic"} {
		t.Run(mode, func(t *testing.T) {
			svc, tasks, db := codexSkillGenerationFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			launches := 0
			directory := ""
			p := &codexAuditProviderFixture{}
			p.stream = func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
				switch mode {
				case "stream":
					return errors.New("private provider payload")
				case "tool":
					return emit(providers.Chunk{ToolCall: &providers.ToolCall{ID: "call", Name: "delegate", Arguments: json.RawMessage(`{}`)}})
				case "cancel":
					cancel()
					return ctx.Err()
				case "panic":
					panic("private provider payload")
				default:
					return emit(providers.Chunk{Text: "private malformed payload", Done: true, FinishReason: "stop"})
				}
			}
			svc.codexLauncher = func(_ context.Context, spec codexbridge.LaunchSpec) (taskProvider, error) {
				launches++
				directory = spec.CWD
				return p, nil
			}
			key := skills.Key{Scope: "project", Name: "failed-workflow"}
			if _, err := svc.GenerateSkillDraft(ctx, "failed-generation", "brain", key, tasks, 0); err == nil {
				t.Fatal("failed generation accepted")
			}
			a, err := db.SkillGenerationAttempt(context.Background(), "failed-generation")
			if err != nil || a.Status != "failed" || a.Result != nil || launches != 1 || p.closed != 1 {
				t.Fatal(a, err, launches, p.closed)
			}
			body, _ := json.Marshal(a)
			if strings.Contains(string(body), "private") {
				t.Fatal("raw failure persisted")
			}
			if _, err := os.Stat(directory); !os.IsNotExist(err) {
				t.Fatal("failed launch directory retained", err)
			}
			if _, err := svc.GenerateSkillDraft(context.Background(), a.ID, "brain", key, tasks, 0); err == nil || launches != 1 {
				t.Fatal("failed attempt redispatched", err)
			}
		})
	}
}

func TestCodexSkillGenerationAdmissionBeforeLaunch(t *testing.T) {
	for _, mode := range []string{"privacy", "private_source", "local_mode", "budget", "context", "estimate", "begin_failure"} {
		t.Run(mode, func(t *testing.T) {
			svc, tasks, _ := codexSkillGenerationFixture(t)
			launches := 0
			svc.codexLauncher = func(context.Context, codexbridge.LaunchSpec) (taskProvider, error) {
				launches++
				return nil, errors.New("unexpected launch")
			}
			switch mode {
			case "privacy":
				svc.settings.Skills.LocalOnly = true
			case "private_source":
				svc.settings.Mode = "local_only"
				svc.settings.Models[0].Locality = "local"
				svc.settings.Models[0].RAMBytes = 1
				svc.settings.Skills.Enabled = false
				svc.settings.Skills.LocalOnly = true
				local, err := NewService(svc.settings, svc.secret)
				if err != nil {
					t.Fatal(err)
				}
				local.profile = svc.profile
				private, err := local.Run(context.Background(), Request{ModelID: "a", Prompt: "Private local creative task", Domain: "creative", Profile: "default"})
				if err != nil {
					t.Fatal(err)
				}
				if err := RecordFeedback(context.Background(), svc.settings.Telemetry.Database, private.TaskID, true, 0); err != nil {
					t.Fatal(err)
				}
				svc.settings.Mode = "hybrid"
				svc.settings.Models[0].Locality = "cloud"
				svc.settings.Skills.Enabled = true
				svc.settings.Skills.LocalOnly = false
				tasks[1] = private.TaskID
			case "local_mode":
				svc.settings.Mode = "local_only"
			case "budget":
				cost := 1.0
				svc.settings.Models[len(svc.settings.Models)-1].EstimatedCost = &cost
			case "context":
				svc.settings.Models[len(svc.settings.Models)-1].ContextTokens = 1
			case "estimate":
				svc.contextEstimator = auxiliaryContextEstimator(func(context.Context, providers.Request) (int, error) { return 0, errors.New("private estimate") })
			case "begin_failure":
				raw, err := sql.Open("sqlite", svc.settings.Telemetry.Database)
				if err != nil {
					t.Fatal(err)
				}
				defer raw.Close()
				if _, err := raw.Exec(`CREATE TRIGGER deny_generation BEFORE INSERT ON skill_generation_attempts BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := svc.GenerateSkillDraft(context.Background(), "denied-generation", "brain", skills.Key{Scope: "project", Name: "workflow"}, tasks, 0); err == nil || launches != 0 {
				t.Fatal("denied generation launched", err, launches)
			}
		})
	}
}

func TestCodexSkillGenerationSelectionAndExecutableBinding(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "executable_changed"}[changed], func(t *testing.T) {
			svc, tasks, _ := codexSkillGenerationFixture(t)
			ctx := context.Background()
			svc.settings.Skills.GenerationBudget.Enabled = true
			svc.settings.Skills.GenerationBudget.MaxAttempts = 1
			selection, err := svc.PlanWorkflowSelection(ctx, "brain", skills.Key{Scope: "project", Name: "selected-workflow"}, "trusted_group", "host_v1", tasks, 0)
			if err != nil {
				t.Fatal(err)
			}
			launches := 0
			p := &codexAuditProviderFixture{stream: func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
				return emit(providers.Chunk{Text: codexSkillDraftJSON, Done: true, FinishReason: "stop"})
			}}
			svc.codexLauncher = func(context.Context, codexbridge.LaunchSpec) (taskProvider, error) { launches++; return p, nil }
			if changed {
				svc.settings.Providers[len(svc.settings.Providers)-1].Executable = "/fixture/different-codex"
			}
			attempt, err := svc.GenerateSkillSelection(ctx, selection.ID, 0)
			if changed {
				if err == nil || launches != 0 {
					t.Fatal("changed executable selection launched", err, launches)
				}
				return
			}
			if err != nil || attempt.Status != "drafted" || launches != 1 || p.closed != 1 {
				t.Fatal(attempt, err, launches, p.closed)
			}
			if _, err := svc.GenerateSkillSelection(ctx, selection.ID, 0); err == nil || launches != 1 {
				t.Fatal("selection redispatched", err)
			}
		})
	}
}
