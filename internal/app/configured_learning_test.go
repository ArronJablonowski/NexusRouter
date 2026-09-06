package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

func configuredRegistry(t *testing.T, v skills.Validator) *skills.ValidatorRegistry {
	t.Helper()
	r, err := skills.NewValidatorRegistry(map[string]skills.Validator{"trusted-v1": v})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func configureNamedLearning(s *Service) {
	s.settings.Skills.Learning.ValidatorID = "trusted-v1"
	s.settings.Skills.Learning.RegressionName = "configured-regression"
	s.settings.Skills.Learning.RegressionInterval = "1s"
}

func TestConfiguredLearningPreflightNoWork(t *testing.T) {
	svc, _, calls := learningFixture(t)
	configureNamedLearning(svc)
	var checks atomic.Int32
	registry := configuredRegistry(t, learningPass(&checks))
	for _, mode := range []string{"nil-registry", "unknown", "rollback-disabled", "missing-catalog", "startup-secret-panic"} {
		t.Run(mode, func(t *testing.T) {
			fresh, err := NewService(svc.settings, svc.secret)
			if err != nil {
				t.Fatal(err)
			}
			fresh.profile, fresh.toolExtension = svc.profile, svc.toolExtension
			r := registry
			switch mode {
			case "nil-registry":
				r = nil
			case "unknown":
				fresh.settings.Skills.Learning.ValidatorID = "unknown"
			case "rollback-disabled":
				fresh.settings.Skills.Rollback = false
			}
			plan, err := PrepareConfiguredLearning(fresh, r)
			if mode == "missing-catalog" || mode == "startup-secret-panic" {
				if err != nil {
					t.Fatal(err)
				}
				if mode == "startup-secret-panic" {
					fresh.secret = func(string) string { panic("private secret lookup") }
				}
				if owned, err := plan.Start(context.Background()); err == nil || owned != nil {
					t.Fatal("unsafe start", err)
				}
			} else if err == nil || plan != nil {
				t.Fatal("invalid preparation accepted")
			}
			if checks.Load() != 0 || calls.Load() != 0 {
				t.Fatal("preflight executed work")
			}
			if _, err := os.Stat(svc.settings.Skills.Root); !os.IsNotExist(err) {
				t.Fatal("preflight created catalog", err)
			}
		})
	}
	disabled, err := NewService(config.Defaults(), nil)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PrepareConfiguredLearning(disabled, nil)
	if err != nil {
		t.Fatal(err)
	}
	owned, err := plan.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	h := owned.Health()
	if len(h) != 1 || h[0].Status != "disabled" {
		t.Fatal(h)
	}
	h[0].Status = "mutated"
	if owned.Health()[0].Status != "disabled" {
		t.Fatal("health aliases")
	}
	if err = owned.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestConfiguredLearningDurablePolicyConflictPreflight(t *testing.T) {
	for _, mode := range []string{"learner", "regression"} {
		t.Run(mode, func(t *testing.T) {
			svc, _, calls := learningFixture(t)
			configureNamedLearning(svc)
			var checks atomic.Int32
			v := learningPass(&checks)
			registry := configuredRegistry(t, v)
			store, err := skills.Open(svc.settings.Skills.Root, []string{svc.settings.Skills.Scope})
			if err != nil {
				t.Fatal(err)
			}
			// A draft initializes a genuine existing catalog without activation.
			_, err = store.Draft(context.Background(), skills.Draft{Key: skills.Key{Scope: svc.settings.Skills.Scope, Name: "baseline"}, Description: "baseline", Steps: []string{"inspect"}, SourceSessions: []string{"operator"}, ValidationCases: []string{"fixture"}}, false)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "regression" {
				store.SetAutomatic(true)
				_, _, err = store.PrepareRegressionMonitor(context.Background(), svc.settings.Skills.Scope, "configured-regression", "trusted-v1", strings.Repeat("a", 64), time.Second, func(context.Context, skills.RegressionMonitorState, skills.RegressionMonitorCheck) error { return nil })
				if err != nil {
					t.Fatal(err)
				}
			}
			store.Close()
			if mode == "learner" {
				db, err := telemetry.Open(context.Background(), svc.settings.Telemetry.Database)
				if err != nil {
					t.Fatal(err)
				}
				err = db.PutLearningState(context.Background(), skills.LearningState{Version: 1, Scope: svc.settings.Skills.Scope, Name: svc.settings.Skills.Learning.Name, Domain: "code", PolicyDigest: strings.Repeat("b", 64), Revision: 1, Phase: "discover"}, 0)
				db.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(filepath.Join(svc.settings.Skills.Root, "catalog.json"))
			if err != nil {
				t.Fatal(err)
			}
			plan, err := PrepareConfiguredLearning(svc, registry)
			if err != nil {
				t.Fatal(err)
			}
			if owned, err := plan.Start(context.Background()); err == nil || owned != nil {
				t.Fatal("durable policy conflict admitted")
			}
			after, err := os.ReadFile(filepath.Join(svc.settings.Skills.Root, "catalog.json"))
			if err != nil || string(after) != string(before) || checks.Load() != 0 || calls.Load() != 0 {
				t.Fatal("preflight mutated or executed", err)
			}
		})
	}
}

func TestConfiguredLearningCloseJoinsValidator(t *testing.T) {
	svc, _, _ := learningFixture(t)
	configureNamedLearning(svc)
	store, err := skills.Open(svc.settings.Skills.Root, []string{svc.settings.Skills.Scope})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	draft := skills.Draft{Key: skills.Key{Scope: svc.settings.Skills.Scope, Name: "baseline"}, Description: "baseline", Steps: []string{"inspect"}, SourceSessions: []string{"operator"}, ValidationCases: []string{"fixture"}}
	v, err := store.Draft(context.Background(), draft, false)
	if err != nil {
		t.Fatal(err)
	}
	var initial atomic.Int32
	if err = store.Activate(context.Background(), draft.Key, v.ID, "", learningPass(&initial), false); err != nil {
		t.Fatal(err)
	}
	entered, left := make(chan struct{}, 1), make(chan struct{}, 1)
	validator := skills.ValidatorFunc(func(ctx context.Context, _ skills.Version) (skills.Evidence, error) {
		entered <- struct{}{}
		<-ctx.Done()
		left <- struct{}{}
		return skills.Evidence{}, ctx.Err()
	})
	plan, err := PrepareConfiguredLearning(svc, configuredRegistry(t, validator))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	owned, err := plan.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer owned.Close()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("regression callback did not start")
	}
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() { defer wg.Done(); _ = owned.Close() }()
	}
	wg.Wait()
	select {
	case <-left:
	default:
		t.Fatal("close abandoned callback")
	}
	for _, h := range owned.Health() {
		if h.Status != "unavailable" {
			t.Fatal(h)
		}
	}
}

func TestConfiguredLearningGeneratesActivatesAndRollsBack(t *testing.T) {
	svc, _, calls := learningFixture(t)
	configureNamedLearning(svc)
	fixture := filepath.Join(t.TempDir(), "lookup.json")
	if err := os.WriteFile(fixture, []byte(`{"lookup":"trusted evidence"}`), 0600); err != nil {
		t.Fatal(err)
	}
	var checks atomic.Int32
	validator := skills.ValidatorFunc(func(ctx context.Context, v skills.Version) (skills.Evidence, error) {
		checks.Add(1)
		if ctx.Err() != nil {
			return skills.Evidence{}, ctx.Err()
		}
		body, err := os.ReadFile(fixture)
		if err != nil {
			return skills.Evidence{}, err
		}
		var result struct {
			Lookup string `json:"lookup"`
		}
		passed := json.Unmarshal(body, &result) == nil && result.Lookup == "trusted evidence" && len(v.Draft.RequiredTools) == 1 && v.Draft.RequiredTools[0] == "lookup"
		return skills.Evidence{ID: "lookup-fixture-v1", Passed: passed, Deterministic: true}, nil
	})
	// Only discovery is primed; drafting, publication, activation and subsequent
	// regression are driven by the configured supervisors' actual scheduled ticks.
	pinned := pinValidatedLearning(t, svc, validator)
	key := skills.Key{Scope: pinned.Scope, Name: pinned.PendingBucketID}
	store, err := skills.Open(svc.settings.Skills.Root, []string{pinned.Scope})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	baseline, err := store.Draft(context.Background(), skills.Draft{Key: key, Description: "baseline lookup", Steps: []string{"Read lookup fixture"}, RequiredTools: []string{"lookup"}, SourceSessions: []string{"operator"}, ValidationCases: []string{"lookup returns trusted evidence"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Activate(context.Background(), key, baseline.ID, "", validator, false); err != nil {
		t.Fatal(err)
	}
	plan, err := PrepareConfiguredLearning(svc, configuredRegistry(t, validator))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	owned, err := plan.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer owned.Close()
	waitState := func(want func(skills.ActivationState) bool) skills.ActivationState {
		t.Helper()
		tick := time.NewTicker(25 * time.Millisecond)
		defer tick.Stop()
		for {
			state, err := store.ActivationState(ctx, key)
			if err == nil && want(state) {
				return state
			}
			select {
			case <-tick.C:
			case <-ctx.Done():
				t.Fatal("configured lifecycle stalled", owned.Health(), err)
			}
		}
	}
	active := waitState(func(s skills.ActivationState) bool { return s.Active != "" && s.Active != baseline.ID })
	if calls.Load() != 1 || checks.Load() < 2 {
		t.Fatal("supervisor did not generate and validate", calls.Load(), checks.Load())
	}
	// A separate service must discover the activated workflow through actual
	// task context assembly, even though the generator returned no domain tags.
	// This qualifies retrieval, not semantic execution of an arbitrary skill.
	consumer, err := NewService(svc.settings, svc.secret)
	if err != nil {
		t.Fatal(err)
	}
	consumer.profile, consumer.toolExtension = svc.profile, svc.toolExtension
	var used atomic.Bool
	consumer.providerFactory = applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		return delegateEstimatorProvider(func(_ context.Context, request providers.Request, emit func(providers.Chunk) error) error {
			for _, message := range request.Messages {
				var selected struct {
					Skills []contextSkill `json:"procedural_skills"`
				}
				if message.Role == "user" && json.Unmarshal([]byte(message.Content), &selected) == nil {
					for _, skill := range selected.Skills {
						if skill.Key == key && skill.Version == active.Active && len(skill.Steps) > 0 {
							used.Store(true)
						}
					}
				}
			}
			return emit(providers.Chunk{Text: "runtime context fixture", Done: true, FinishReason: "stop"})
		}), nil
	})
	if _, err = consumer.Run(ctx, Request{ModelID: "a", Prompt: "Inspect the lookup result", Domain: "code"}); err != nil || !used.Load() {
		t.Fatal("activated generated skill was not loaded into runtime context", err)
	}
	if err = os.WriteFile(fixture, []byte(`{"lookup":"regressed result"}`), 0600); err != nil {
		t.Fatal(err)
	}
	rolled := waitState(func(s skills.ActivationState) bool { return s.Active == baseline.ID && s.Revision != active.Revision })
	_ = owned.Close()
	if rolled.Active != baseline.ID {
		t.Fatal(rolled)
	}
	fresh, err := NewService(svc.settings, svc.secret)
	if err != nil {
		t.Fatal(err)
	}
	fresh.profile, fresh.toolExtension = svc.profile, svc.toolExtension
	if reopened, err := fresh.SkillActivationState(ctx, key); err != nil || reopened != rolled {
		t.Fatal("restart lost rollback", reopened, err)
	}
	previousMonitor, err := fresh.SkillRegressionMonitorState(ctx, "configured-regression")
	if err != nil {
		t.Fatal("restart lost durable monitor", err)
	}
	if err = os.WriteFile(fixture, []byte(`{"lookup":"trusted evidence"}`), 0600); err != nil {
		t.Fatal(err)
	}
	restartPlan, err := PrepareConfiguredLearning(fresh, configuredRegistry(t, validator))
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := restartPlan.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		state, err := fresh.SkillRegressionMonitorState(ctx, "configured-regression")
		if err == nil && state.Revision > previousMonitor.Revision {
			break
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			t.Fatal("restarted configured monitor stalled", err)
		}
	}
	_ = restarted.Close()
	if state, err := fresh.SkillActivationState(ctx, key); err != nil || state != rolled || calls.Load() != 1 {
		t.Fatal("restart reactivated or regenerated work", state, err, calls.Load())
	}
	if errors.Is(owned.Close(), context.DeadlineExceeded) {
		t.Fatal("lifecycle only ended by timeout")
	}
}
