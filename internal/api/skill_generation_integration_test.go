package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/skills"
)

func TestSkillGenerationHTTPApplicationLifecycle(t *testing.T) {
	ctx := context.Background()
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		text := "A creative response"
		if calls.Add(1) > 2 {
			text = `{"version":1,"description":"Reusable workflow","tags":[],"steps":["Inspect user requirements"],"required_tools":[],"configuration":"","risks":[],"validation_cases":["Check requirements"]}`
		}
		fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":true,\"done_reason\":\"stop\"}\n", text)
	}))
	defer provider.Close()
	cfg := config.Defaults()
	cfg.Mode = "cloud_only" // All fixture traffic remains loopback.
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "generation.db")
	cfg.Providers = []config.Provider{{ID: "fixture", Kind: "ollama", Endpoint: provider.URL}}
	zero := 0.0
	cfg.Models = []config.Model{{ID: "generator.alias", Provider: "fixture", Model: "underlying-model", Locality: "cloud", Capabilities: []string{"chat"}, ContextTokens: 32768, EstimatedCost: &zero}}
	var tasks []string
	for range 2 {
		result, err := app.RunExplicit(ctx, cfg, app.Request{ModelID: "generator.alias", Prompt: "Write a creative response", Domain: "creative"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := app.RecordFeedback(ctx, cfg.Telemetry.Database, result.TaskID, true, 0); err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, result.TaskID)
	}
	cfg.Skills.Enabled, cfg.Skills.AutoDraft, cfg.Skills.LocalOnly = true, true, false
	cfg.Skills.Scope = "project"
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg.Skills.Root = filepath.Join(root, "skills")
	svc, err := app.NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := services()
	s.GenerateSkillDraft = func(ctx context.Context, id, model, name string, tasks []string, cost float64) (skills.GenerationAttempt, error) {
		return svc.GenerateSkillDraft(ctx, id, model, skills.Key{Scope: cfg.Skills.Scope, Name: name}, tasks, cost)
	}
	s.PublishSkillGeneration = svc.PublishSkillGeneration
	s.DiscoverSkillWorkflows = svc.DiscoverSkillWorkflows
	h, err := New(token, 1, s)
	if err != nil {
		t.Fatal(err)
	}
	// Discover eligible sources through the same authenticated API before drafting.
	discovered := httptest.NewRecorder()
	h.ServeHTTP(discovered, request("GET", "/v1/skills/workflows?domain=creative&scan_limit=20", ""))
	var page skills.WorkflowCandidatePage
	if discovered.Code != 200 || json.Unmarshal(discovered.Body.Bytes(), &page) != nil || page.Validate("", 20) != nil || len(page.Candidates) != 2 || calls.Load() != 2 {
		t.Fatal("discovery failed or dispatched", discovered.Code, discovered.Body.String())
	}
	tasks = nil
	for _, candidate := range page.Candidates {
		tasks = append(tasks, candidate.TaskID)
	}
	body, _ := json.Marshal(map[string]any{"version": 1, "id": "generation", "model_id": "generator.alias", "name": "workflow", "task_ids": tasks})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request("POST", "/v1/skills/generations", string(body)))
	var a skills.GenerationAttempt
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &a) != nil || a.Validate() != nil || a.Status != "drafted" || a.Key.Scope != "project" || a.Model != "underlying-model" {
		t.Fatal("generation failed", w.Code, w.Body.String())
	}
	db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	saved, err := db.SkillGenerationAttempt(ctx, a.ID)
	if err != nil || !reflect.DeepEqual(saved, a) {
		t.Fatal("generation not persisted", err)
	}
	if _, err := os.Stat(cfg.Skills.Root); !os.IsNotExist(err) {
		t.Fatal("generation published implicitly", err)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, request("POST", "/v1/skills/generations", string(body)))
	if w.Code != 422 || calls.Load() != 3 {
		t.Fatal("duplicate generation dispatched", w.Code, calls.Load())
	}
	var first skills.Version
	for i := range 2 {
		w = httptest.NewRecorder()
		h.ServeHTTP(w, request("POST", "/v1/skills/generations/generation/publish", `{"version":1}`))
		var v skills.Version
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil || v.Validate() != nil {
			t.Fatal("publication failed", w.Code, w.Body.String())
		}
		if i == 0 {
			first = v
		} else if !reflect.DeepEqual(first, v) {
			t.Fatal("publication duplicated version")
		}
	}
	store, err := skills.OpenReadOnly(cfg.Skills.Root, []string{"project"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	history, err := store.History(ctx, a.Key)
	if err != nil || history.Active != "" || len(history.Versions) != 1 || calls.Load() != 3 {
		t.Fatal("publication activated or dispatched", history, err)
	}
	// Missing source evidence must not become a new model call or durable attempt.
	bad := strings.Replace(string(body), `"generation"`, `"other-generation"`, 1)
	bad = strings.Replace(bad, tasks[0], "missing-task", 1)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, request("POST", "/v1/skills/generations", bad))
	if w.Code != 422 || calls.Load() != 3 {
		t.Fatal("missing source admitted", w.Code, calls.Load())
	}
	if _, err := db.SkillGenerationAttempt(ctx, "other-generation"); err == nil {
		t.Fatal("denied source created attempt")
	}
}
