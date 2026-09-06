package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/internal/codexbridge"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

// Explicitly supervised live generation uses only accepted synthetic loopback
// responses. The ordinary test suite never launches the signed-in CLI here.
func TestLiveCodexSkillDraft(t *testing.T) {
	if os.Getenv("DARWIN_CODEX_LIVE_SKILL_DRAFT") != "1" {
		t.Skip("explicit supervised signed-in Sol skill generation only")
	}
	bin, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal("CLI unavailable")
	}
	bin, err = filepath.Abs(bin)
	if err != nil {
		t.Fatal("CLI path unavailable")
	}
	var sourceCalls atomic.Int32
	loopback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sourceCalls.Add(1) == 1 {
			fmt.Fprintln(w, `{"message":{"content":"package arithmetic\nfunc Square(n int) int { return n * n }"},"done":true,"done_reason":"stop"}`)
		} else {
			fmt.Fprintln(w, `{"message":{"content":"package arithmetic\nfunc Cube(n int) int { return n * n * n }"},"done":true,"done_reason":"stop"}`)
		}
	}))
	defer loopback.Close()
	cfg := codexTaskConfig(t)
	cfg.Providers = append(cfg.Providers, config.Provider{ID: "synthetic-local", Kind: "ollama", Endpoint: loopback.URL})
	zero := 0.0
	cfg.Models = append(cfg.Models, config.Model{ID: "synthetic", Provider: "synthetic-local", Model: "synthetic-go", Locality: "cloud", Capabilities: []string{"chat"}, RAMBytes: 1, ContextTokens: 4096, EstimatedCost: &zero})
	cfg.Skills.Enabled, cfg.Skills.AutoDraft, cfg.Skills.LocalOnly = false, true, false
	cfg.Skills.Scope, cfg.Skills.Root = "project", filepath.Join(t.TempDir(), "unpublished-live-skills")
	svc, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal("synthetic generation configuration invalid")
	}
	svc.profile = healthProfile
	var tasks []string
	for _, prompt := range []string{"Return complete Go source in package arithmetic implementing Square(n int) int using n * n. Return code only; do not claim tests were run.", "Return complete Go source in package arithmetic implementing Cube(n int) int using n * n * n. Return code only; do not claim tests were run."} {
		result, err := svc.Run(context.Background(), Request{ModelID: "synthetic", Prompt: prompt, Domain: "code", Profile: "default", Validation: "go_source"})
		if err != nil {
			t.Fatal("synthetic source creation failed")
		}
		if err := RecordFeedback(context.Background(), svc.settings.Telemetry.Database, result.TaskID, true, 0); err != nil {
			t.Fatal("synthetic acceptance recording failed")
		}
		tasks = append(tasks, result.TaskID)
	}
	if sourceCalls.Load() != 2 {
		t.Fatal("unexpected synthetic source execution count")
	}
	// Source execution must not attempt skill discovery in the deliberately
	// nonexistent publication root. Enable drafting only after accepted sources.
	svc.settings.Skills.Enabled = true
	db, err := telemetry.OpenReadOnly(context.Background(), svc.settings.Telemetry.Database)
	if err != nil {
		t.Fatal("synthetic source inspection unavailable")
	}
	defer db.Close()
	cost := 0.1
	providerID := ""
	for i := range svc.settings.Models {
		if svc.settings.Models[i].ID == "brain" {
			svc.settings.Models[i].EstimatedCost = &cost
			providerID = svc.settings.Models[i].Provider
			if svc.settings.Models[i].Model != "gpt-5.6-sol" {
				t.Fatal("fixture coordinator mismatch")
			}
		}
	}
	if providerID == "" {
		t.Fatal("fixture brain missing")
	}
	for i := range svc.settings.Providers {
		if svc.settings.Providers[i].ID == providerID {
			svc.settings.Providers[i].Executable = bin
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	before, err := db.SkillWorkflowSources(ctx, tasks)
	if err != nil || len(before) != 2 {
		t.Fatal("synthetic sources unavailable")
	}
	var sessions, evidence []string
	for _, source := range before {
		if source.Privacy != "cloud_allowed" {
			t.Fatal("fixture not cloud eligible")
		}
		sessions = append(sessions, source.Example.SessionID)
		resolved, err := evaluation.Resolve(source.Example.Checks, false)
		if err != nil || !resolved.Accepted {
			t.Fatal("fixture evidence not accepted")
		}
		evidence = append(evidence, resolved.References...)
	}
	slices.Sort(sessions)
	sessions = slices.Compact(sessions)
	slices.Sort(evidence)
	evidence = slices.Compact(evidence)
	observed := &auditLiveObservedProvider{}
	launches := 0
	var directory string
	svc.codexLauncher = func(callCtx context.Context, spec codexbridge.LaunchSpec) (taskProvider, error) {
		launches++
		directory = spec.CWD
		if spec.Model != "gpt-5.6-sol" || spec.Privacy != "cloud_allowed" {
			t.Fatal("native launch admission mismatch")
		}
		attempt, err := db.SkillGenerationAttempt(callCtx, "live-codex-skill")
		if err != nil || attempt.Status != "started" {
			t.Fatal("launch preceded durable claim")
		}
		p, err := codexbridge.LaunchChecked(callCtx, spec)
		if err != nil {
			return nil, err
		}
		observed.taskProvider = p
		return observed, nil
	}
	key := skills.Key{Scope: "project", Name: "live-codex-workflow"}
	attempt, generationErr := svc.GenerateSkillDraft(ctx, "live-codex-skill", "brain", key, tasks, cost)
	t.Logf("live skill protocol metadata: launches=%d streams=%d done=%t bytes=%d failure=%s", launches, observed.streams, observed.done, observed.bytes, observed.failure)
	if directory != "" {
		if _, err := os.Stat(directory); !os.IsNotExist(err) {
			t.Fatal("owned generation directory retained")
		}
	}
	if _, err := os.Stat(svc.settings.Skills.Root); !os.IsNotExist(err) {
		t.Fatal("generation published or activated a skill")
	}
	if generationErr != nil {
		t.Fatal("live Sol skill generation failed; payload withheld")
	}
	if launches != 1 || observed.streams != 1 || !observed.done || directory == "" || attempt.Model != "gpt-5.6-sol" || attempt.Status != "drafted" || attempt.Result == nil || attempt.Result.Draft.Key != key || !slices.Equal(attempt.Result.Draft.SourceSessions, sessions) || !slices.Equal(attempt.Result.Draft.SourceEvidence, evidence) {
		t.Fatal("live generation metadata or host provenance mismatch")
	}
	saved, err := db.SkillGenerationAttempt(ctx, attempt.ID)
	if err != nil || !reflect.DeepEqual(saved, attempt) {
		t.Fatal("generated proposal not durable")
	}
	after, err := db.SkillWorkflowSources(ctx, tasks)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("generation changed accepted sources")
	}
	t.Log("live skill result: drafted=true durable=true source_unchanged=true published=false activated=false")
}
