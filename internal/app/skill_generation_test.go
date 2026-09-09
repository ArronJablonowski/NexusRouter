package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

func skillGenerationAppFixture(t *testing.T) (*Service, []string) {
	t.Helper()
	svc, cfg := autoFixture(t)
	var tasks []string
	for range 2 {
		result, err := svc.Run(context.Background(), Request{ModelID: "a", Prompt: "Write a creative response involving runtime-token and provider-token", Domain: "creative"})
		if err != nil {
			t.Fatal(err)
		}
		if err := RecordFeedback(context.Background(), cfg.Telemetry.Database, result.TaskID, true, 0); err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, result.TaskID)
	}
	svc.settings.Skills.Root = filepath.Join(t.TempDir(), "unpublished-skills")
	svc.settings.Skills.Scope = "project"
	svc.settings.Skills.Enabled, svc.settings.Skills.AutoDraft = true, true
	return svc, tasks
}

func TestGenerateSkillDraftUsesVerifiedHistoryAndRedactsBeforePersistence(t *testing.T) {
	svc, tasks := skillGenerationAppFixture(t)
	ctx := context.Background()
	db, err := telemetry.OpenReadOnly(ctx, svc.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	before, err := db.SkillWorkflowSources(ctx, tasks)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		a, err := db.SkillGenerationAttempt(ctx, "generation")
		if err != nil || a.Status != "started" {
			t.Error("dispatch before start", a, err)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || strings.Contains(string(body), "runtime-token") || strings.Contains(string(body), "provider-token") || !strings.Contains(string(body), "[REDACTED]") {
			t.Error("unredacted model input", err)
		}
		const draft = `{"version":1,"description":"Reusable creative workflow","tags":["creative"],"steps":["Inspect runtime-token and provider-token requirements"],"required_tools":[],"configuration":"provider-token","risks":["Respect user taste"],"validation_cases":["Check explicit user constraints"]}`
		fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":true,\"done_reason\":\"stop\",\"prompt_eval_count\":10,\"eval_count\":20}\n", draft)
	}))
	defer server.Close()
	svc.settings.Providers[0].Endpoint, svc.settings.Providers[0].APIKeyEnv = server.URL, "GEN_KEY"
	svc.secret = func(name string) string {
		if name == "DARWIN_API_TOKEN" {
			return "runtime-token"
		}
		if name == "GEN_KEY" {
			return "provider-token"
		}
		return ""
	}
	key := skills.Key{Scope: "project", Name: "creative-workflow"}
	a, err := svc.GenerateSkillDraft(ctx, "generation", "a", key, tasks, 0)
	if err != nil || a.Status != "drafted" || a.Result == nil || a.Result.Draft.Privacy != skills.PrivacyLocalOnly || calls.Load() != 1 {
		t.Fatal(a, err, calls.Load())
	}
	body, _ := json.Marshal(a)
	if strings.Contains(string(body), "runtime-token") || strings.Contains(string(body), "provider-token") || !strings.Contains(string(body), "[REDACTED]") {
		t.Fatal("result not redacted")
	}
	saved, err := db.SkillGenerationAttempt(ctx, a.ID)
	if err != nil || !reflect.DeepEqual(saved, a) {
		t.Fatal("returned unsaved result", saved, err)
	}
	after, err := db.SkillWorkflowSources(ctx, tasks)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("source changed", err)
	}
	if _, err := svc.GenerateSkillDraft(ctx, "generation", "a", key, tasks, 0); err == nil || calls.Load() != 1 {
		t.Fatal("duplicate dispatch", err, calls.Load())
	}
	if _, err := os.Stat(svc.settings.Skills.Root); !os.IsNotExist(err) {
		t.Fatal("proposal published into skill store", err)
	}
}

func TestGenerateSkillDraftRejectsPolicyBeforeAttempt(t *testing.T) {
	for _, mode := range []string{"disabled", "auto-disabled", "scope", "cloud", "unknown-model", "unknown-cost", "unknown-context"} {
		t.Run(mode, func(t *testing.T) {
			svc, tasks := skillGenerationAppFixture(t)
			model := "a"
			key := skills.Key{Scope: "project", Name: "workflow"}
			switch mode {
			case "disabled":
				svc.settings.Skills.Enabled = false
			case "auto-disabled":
				svc.settings.Skills.AutoDraft = false
			case "scope":
				key.Scope = "other"
			case "cloud":
				svc.settings.Models[0].Locality = "cloud"
			case "unknown-model":
				model = "missing"
			case "unknown-cost":
				svc.settings.Models[0].EstimatedCost = nil
			case "unknown-context":
				svc.settings.Models[0].ContextTokens = 0
			}
			if _, err := svc.GenerateSkillDraft(context.Background(), "denied", model, key, tasks, 0); err == nil {
				t.Fatal("admitted", mode)
			}
			db, err := telemetry.OpenReadOnly(context.Background(), svc.settings.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			attempts, err := db.ListSkillGenerationAttempts(context.Background(), "project", "", 100)
			if err != nil || len(attempts) != 0 {
				t.Fatal("denied attempt persisted", attempts, err)
			}
		})
	}
}
