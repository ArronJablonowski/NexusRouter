package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
)

func TestSkillTaskOutcomeActualContextAndFeedback(t *testing.T) {
	ctx := context.Background()
	svc, cfg := autoFixture(t)
	store, settings := contextSkillStore(t)
	v := seedContextSkill(t, store, "project", "workflow", "creative", "private-workflow-body", nil, true)
	svc.settings.Skills = settings
	var calls atomic.Int32
	svc.providerFactory = applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		return delegateEstimatorProvider(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
			calls.Add(1)
			return emit(providers.Chunk{Text: "private-result-body", Done: true, FinishReason: "stop"})
		}), nil
	})
	result, err := svc.Run(ctx, Request{ModelID: "a", Domain: "creative", Prompt: "private-task-body"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := svc.SkillTaskOutcome(ctx, result.TaskID)
	if err != nil || out.Validate() != nil || out.SkillContext == nil || len(out.SkillContext.References) != 1 || out.SkillContext.References[0].Version != v.ID || out.Quality != nil || len(out.OutputChecks) != 1 || !out.OutputChecks[0].Passed {
		t.Fatal("valid text was not separated from unknown quality", out, err)
	}
	if err = RecordFeedback(ctx, cfg.Telemetry.Database, result.TaskID, false, 0); err != nil {
		t.Fatal(err)
	}
	negative, err := svc.SkillTaskOutcome(ctx, result.TaskID)
	if err != nil || negative.Quality == nil || negative.Quality.Accepted || negative.Quality.Source != evaluation.UserFeedback {
		t.Fatal(negative, err)
	}
	if err = ReviseFeedback(ctx, cfg.Telemetry.Database, result.TaskID, negative.EvaluationID, true); err != nil {
		t.Fatal(err)
	}
	// Inspection remains useful when learning/retrieval is turned off. The
	// original catalog may disappear without relabeling the task's exposure.
	svc.settings.Skills.Enabled = false
	svc.settings.Skills.Root = filepath.Join(t.TempDir(), "not-created")
	db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	before, err := db.Read(ctx, result.TaskID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	positive, err := svc.SkillTaskOutcome(ctx, result.TaskID)
	if err != nil || positive.Quality == nil || !positive.Quality.Accepted || positive.EvaluationID == negative.EvaluationID || positive.EvaluationDigest == negative.EvaluationDigest || calls.Load() != 1 {
		t.Fatal(positive, err)
	}
	body, _ := json.Marshal(positive)
	if strings.Contains(string(body), "private-") {
		t.Fatal("metadata disclosed task/skill/output text")
	}
	positive.SkillContext.References[0].Name = "caller-change"
	again, err := svc.SkillTaskOutcome(ctx, result.TaskID)
	if err != nil || again.SkillContext.References[0].Name != "workflow" {
		t.Fatal("returned state alias", err)
	}
	after, err := db.Read(ctx, result.TaskID, 0, 100)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("inspection mutated journal", err)
	}
	if _, err = os.Stat(svc.settings.Skills.Root); !os.IsNotExist(err) {
		t.Fatal("inspection opened catalog", err)
	}
	svc.settings.Skills.Scope = "other"
	if out, err := svc.SkillTaskOutcome(ctx, result.TaskID); err == nil || out.TaskID != "" {
		t.Fatal("foreign scope leaked")
	}
	svc.settings.Skills.Scope = "project"
	svc.secret = func(string) string { return "workflow" }
	if out, err := svc.SkillTaskOutcome(ctx, result.TaskID); err == nil || out.TaskID != "" {
		t.Fatal("rotated identity secret leaked")
	}
}

func TestSkillTaskOutcomeGuardsNoStorage(t *testing.T) {
	cfg := config.Defaults()
	cfg.Skills.Scope = "project"
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "missing.db")
	cfg.Skills.Root = filepath.Join(t.TempDir(), "missing-skills")
	svc, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, test := range []struct {
		service *Service
		ctx     context.Context
		task    string
	}{
		{nil, context.Background(), "task"}, {svc, nil, "task"}, {svc, ctx, "task"},
		{svc, context.Background(), "bad:id"}, {svc, context.Background(), "task"},
	} {
		if out, err := test.service.SkillTaskOutcome(test.ctx, test.task); err == nil || out.TaskID != "" {
			t.Fatal("invalid inspection accepted")
		}
	}
	svc.secret = func(string) string { panic("private-secret-error") }
	if out, err := svc.SkillTaskOutcome(context.Background(), "task"); err == nil || out.TaskID != "" {
		t.Fatal("secret callback panic escaped")
	}
	if _, err := os.Stat(cfg.Telemetry.Database); !os.IsNotExist(err) {
		t.Fatal("created missing storage", err)
	}
}

func TestSkillTaskOutcomeWithoutConfiguredSkills(t *testing.T) {
	svc, _ := autoFixture(t)
	svc.settings.Skills.Enabled = false
	svc.settings.Skills.Root, svc.settings.Skills.Scope = "", ""
	result, err := svc.Run(context.Background(), Request{ModelID: "a", Prompt: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := svc.SkillTaskOutcome(context.Background(), result.TaskID)
	if err != nil || out.Validate() != nil || out.SkillContext == nil || !out.SkillContext.Complete || len(out.SkillContext.References) != 0 {
		t.Fatal("inspection required an unrelated skill configuration", out, err)
	}
}
