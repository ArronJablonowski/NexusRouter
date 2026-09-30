package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/skills"
)

func learningFixture(t *testing.T) (*Service, []string, *atomic.Int32) {
	t.Helper()
	svc, tasks, _ := groupedAppFixture(t)
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc.settings.Skills.Root = filepath.Join(parent, "catalog")
	calls := new(atomic.Int32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/api/chat" {
			t.Error("unexpected learning provider path")
		}
		text := `{"version":1,"description":"Inspect with lookup","tags":[],"steps":["Look up the input","Check the result"],"required_tools":["lookup"],"configuration":"","risks":[],"validation_cases":["Validate the lookup result"]}`
		json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": text}, "done": true, "done_reason": "stop"})
	}))
	t.Cleanup(server.Close)
	svc.providerFactory = nil
	svc.settings.Providers[0].Endpoint = server.URL
	svc.settings.Skills.GenerationBudget.Enabled = true
	svc.settings.Skills.GenerationBudget.MaxAttempts = 1
	svc.settings.Skills.GenerationBudget.Cooldown = "0s"
	l := &svc.settings.Skills.Learning
	l.Enabled = true
	l.Name = "scheduler"
	l.Domain = "code"
	l.ModelID = "a"
	l.ScanLimit = 20
	l.Interval = "1s"
	if err := svc.settings.Validate(); err != nil {
		t.Fatal(err)
	}
	return svc, tasks, calls
}

func pinLearning(t *testing.T, svc *Service) skills.LearningState {
	t.Helper()
	var state skills.LearningState
	for range 8 {
		var err error
		state, err = svc.LearningStep(context.Background())
		if err != nil {
			t.Fatal(state, err)
		}
		if state.PendingSelectionID != "" {
			return state
		}
	}
	t.Fatal("selection never pinned")
	return state
}

func TestLearningPipelineHTTPDraftInactiveAndRestart(t *testing.T) {
	svc, _, calls := learningFixture(t)
	ctx := context.Background()
	pinned := pinLearning(t, svc)
	if calls.Load() != 0 {
		t.Fatal("planning invoked provider")
	}
	state, err := svc.LearningStep(ctx)
	if err != nil || state.PendingSelectionID != pinned.PendingSelectionID || calls.Load() != 1 {
		t.Fatal(state, err, calls.Load())
	}
	// Simulate a new Service: all recovery decisions must use durable ledgers.
	fresh, err := NewService(svc.settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	fresh.profile = svc.profile
	fresh.toolExtension = svc.toolExtension
	state, err = fresh.LearningStep(ctx)
	if err != nil || state.PendingSelectionID != "" || state.BucketAfter != pinned.PendingBucketID || calls.Load() != 1 {
		t.Fatal(state, err, calls.Load())
	}
	store, err := skills.Open(svc.settings.Skills.Root, []string{svc.settings.Skills.Scope})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key := skills.Key{Scope: svc.settings.Skills.Scope, Name: pinned.PendingBucketID}
	versions, err := store.History(ctx, key)
	if err != nil || len(versions.Versions) != 1 {
		t.Fatal(versions, err)
	}
	activation, err := store.ActivationState(ctx, key)
	if err == nil && activation.Active != "" {
		t.Fatal("learning activated unvalidated draft")
	}
	for range 8 {
		if _, err = fresh.LearningStep(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("restart redispatched completed selection")
	}
}

func TestLearningDisabledAndPolicyDrift(t *testing.T) {
	svc, cfg := autoFixture(t)
	ctx := context.Background()
	l, err := StartLearning(ctx, svc)
	if err != nil || l.Health().Code != "disabled_by_policy" || l.Health().Validate() != nil {
		t.Fatal(l, err)
	}
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.LearningStep(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(cfg.Telemetry.Database); !os.IsNotExist(err) {
		t.Fatal("disabled scheduler created storage")
	}
	enabled, _, calls := learningFixture(t)
	original, err := enabled.LearningStep(ctx)
	if err != nil {
		t.Fatal(err)
	}
	enabled.settings.Skills.Learning.Interval = "2s"
	if _, err = enabled.LearningStep(ctx); err != ErrLearningAttention {
		t.Fatal("policy drift accepted")
	}
	enabled.settings.Skills.Learning.Enabled = false
	saved, err := enabled.SkillLearningState(ctx)
	if err != nil || !reflect.DeepEqual(saved, original) || calls.Load() != 0 {
		t.Fatal(saved, err)
	}
}

func TestLearningPublicationFailureNeverRegenerates(t *testing.T) {
	svc, _, calls := learningFixture(t)
	ctx := context.Background()
	state := pinLearning(t, svc)
	db, err := telemetry.Open(ctx, svc.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// A completed generation is inspected independently; failing publication is
	// never permission to run generation again.
	if _, err = svc.LearningStep(ctx); err != nil {
		t.Fatal(err)
	}
	before, err := db.SkillGenerationAttempt(ctx, state.PendingSelectionID)
	if err != nil || before.Status != "drafted" {
		t.Fatal(before, err)
	}
	if err = os.WriteFile(svc.settings.Skills.Root, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err = svc.LearningStep(ctx); err != ErrLearningAttention {
			t.Fatal("publication failure ignored", err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("publication failure regenerated")
	}
}

func TestLearningStartedClaimStopsWithoutRedispatch(t *testing.T) {
	svc, _, calls := learningFixture(t)
	ctx := context.Background()
	state := pinLearning(t, svc)
	raw, err := sql.Open("sqlite", svc.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err = raw.Exec(`CREATE TRIGGER deny_generation_terminal BEFORE UPDATE OF status ON skill_generation_attempts WHEN NEW.status <> 'started' BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.LearningStep(ctx); err != ErrLearningAttention {
		t.Fatal("terminal persistence failure ignored", err)
	}
	if _, err = raw.Exec("DROP TRIGGER deny_generation_terminal"); err != nil {
		t.Fatal(err)
	}
	read, err := telemetry.OpenReadOnly(ctx, svc.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	attempt, err := read.SkillGenerationAttempt(ctx, state.PendingSelectionID)
	if err != nil || attempt.Status != "started" || calls.Load() != 1 {
		t.Fatal(attempt, err, calls.Load())
	}
	fresh, err := NewService(svc.settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	fresh.profile = svc.profile
	fresh.toolExtension = svc.toolExtension
	for range 2 {
		got, err := fresh.LearningStep(ctx)
		if err != ErrLearningAttention || !reflect.DeepEqual(got, state) {
			t.Fatal(got, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("started uncertain attempt redispatched")
	}
}

func TestLearningAggregateBudgetDenialKeepsPinnedIdentity(t *testing.T) {
	svc, tasks, calls := learningFixture(t)
	ctx := context.Background()
	selection, err := svc.PlanGroupedWorkflowSelection(ctx, "a", skills.Key{Scope: svc.settings.Skills.Scope, Name: "budget-consuming-draft"}, tasks, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.GenerateSkillSelection(ctx, selection.ID, 0); err != nil {
		t.Fatal(err)
	}
	pinned := pinLearning(t, svc)
	for range 2 {
		got, err := svc.LearningStep(ctx)
		if !errors.Is(err, skills.ErrGenerationBudget) || !reflect.DeepEqual(got, pinned) {
			t.Fatal(got, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("budget denial dispatched")
	}
	l, err := StartLearning(ctx, svc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := l.Close(); err != nil {
			t.Error(err)
		}
	})
	deadline := time.After(3 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for l.Health().Code != "learning_budget_wait" {
		select {
		case <-deadline:
			t.Fatal("budget wait health not reported", l.Health())
		case <-ticker.C:
		}
	}
	if check := l.Health(); check.Status != "healthy" || check.Validate() != nil {
		t.Fatal(check)
	}
	got, err := svc.SkillLearningState(ctx)
	if err != nil || !reflect.DeepEqual(got, pinned) || calls.Load() != 1 {
		t.Fatal(got, err, calls.Load())
	}
}

func TestLearningSupervisorClosesAndJoins(t *testing.T) {
	svc, _, _ := learningFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	l, err := StartLearning(ctx, svc)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	done := make(chan error, 1)
	go func() { done <- l.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("learning shutdown failed to join")
	}
	if l.Health().Code != "supervisor_stopped" || l.Health().Validate() != nil {
		t.Fatal(l.Health())
	}
}
