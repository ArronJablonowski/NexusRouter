package v1_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/ArronJablonowski/DarwinRouter/sdk/v1"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

func sdkRegressionMonitorFixture(t *testing.T) (*sdk.Client, sdk.ConfigOptions, skills.Version, skills.Version, string) {
	t.Helper()
	options, _ := sdkToolOptions(t)
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "catalog")
	options.Overrides = map[string]string{"skills.enabled": "true", "skills.rollback_on_regression": "true", "skills.auto_activate_after_validation": "false", "skills.learning.enabled": "false", "skills.root": root, "skills.scope": "project"}
	store, err := skills.Open(root, []string{"project"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	draft := skills.Draft{Key: skills.Key{Scope: "project", Name: "workflow"}, Description: "Workflow", SourceSessions: []string{"session"}, Steps: []string{"Inspect"}, ValidationCases: []string{"Fixture"}}
	pass := skills.ValidatorFunc(func(context.Context, skills.Version) (skills.Evidence, error) {
		return skills.Evidence{ID: "pass", Passed: true, Deterministic: true}, nil
	})
	first, err := store.Draft(ctx, draft, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Activate(ctx, draft.Key, first.ID, "", pass, false); err != nil {
		t.Fatal(err)
	}
	draft.Steps = []string{"Revised inspection"}
	second, err := store.Draft(ctx, draft, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Activate(ctx, draft.Key, second.ID, first.ID, pass, false); err != nil {
		t.Fatal(err)
	}
	client, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	return client, options, first, second, filepath.Join(root, "catalog.json")
}

func TestSDKSkillRegressionStepAndRollback(t *testing.T) {
	client, _, first, second, path := sdkRegressionMonitorFixture(t)
	ctx := context.Background()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	pass := skills.ValidatorFunc(func(context.Context, skills.Version) (skills.Evidence, error) {
		calls++
		return skills.Evidence{ID: "pass", Passed: true, Deterministic: true}, nil
	})
	next, err := client.SkillRegressionStep(ctx, "", pass)
	if err != nil || next != "workflow" || calls != 1 {
		t.Fatal(next, err, calls)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("passing step changed catalog", err)
	}
	next, err = client.SkillRegressionStep(ctx, next, pass)
	if err != nil || next != "" || calls != 1 {
		t.Fatal("empty page did not wrap", next, err)
	}
	fail := skills.ValidatorFunc(func(_ context.Context, v skills.Version) (skills.Evidence, error) {
		calls++
		if v.ID != second.ID {
			t.Error("wrong candidate")
		}
		return skills.Evidence{ID: "regression", Passed: false, Deterministic: true}, nil
	})
	next, err = client.SkillRegressionStep(ctx, "", fail)
	if err != nil || next != "workflow" || calls != 2 {
		t.Fatal(next, err, calls)
	}
	state, err := client.SkillActivationState(ctx, first.Draft.Key)
	if err != nil || state.Active != first.ID {
		t.Fatal("rollback not forwarded", state, err)
	}
	bad := skills.ValidatorFunc(func(context.Context, skills.Version) (skills.Evidence, error) {
		return skills.Evidence{}, errors.New("private-failure")
	})
	next, err = client.SkillRegressionStep(ctx, "", bad)
	if !errors.Is(err, sdk.ErrAdmission) || next != "workflow" {
		t.Fatal("failed key cursor not advanced", next, err)
	}
}

func TestSDKSkillRegressionMonitorStartsAndJoins(t *testing.T) {
	client, _, _, _, path := sdkRegressionMonitorFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	validator := skills.ValidatorFunc(func(context.Context, skills.Version) (skills.Evidence, error) {
		calls.Add(1)
		return skills.Evidence{ID: "pass", Passed: true, Deterministic: true}, nil
	})
	monitor, err := client.StartSkillRegression(ctx, time.Hour, validator)
	if err != nil {
		t.Fatal(err)
	}
	defer monitor.Close()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for calls.Load() != 1 || monitor.Health().Status != "healthy" {
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("monitor did not check", monitor.Health())
		}
	}
	if monitor.Health().Component != "skill_regression" {
		t.Fatal(monitor.Health())
	}
	if err := monitor.Close(); err != nil {
		t.Fatal(err)
	}
	if err := monitor.Close(); err != nil {
		t.Fatal(err)
	}
	if monitor.Health().Status != "unavailable" {
		t.Fatal("monitor not joined", monitor.Health())
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("passing monitor changed catalog", err)
	}
}

func TestSDKSkillRegressionMonitorAdmission(t *testing.T) {
	client, options, _, _, _ := sdkRegressionMonitorFixture(t)
	ctx := context.Background()
	valid := skills.ValidatorFunc(func(context.Context, skills.Version) (skills.Evidence, error) {
		t.Error("invalid request called validator")
		return skills.Evidence{}, nil
	})
	for _, invalid := range []*sdk.Client{nil, {}} {
		if next, err := invalid.SkillRegressionStep(ctx, "cursor", valid); !errors.Is(err, sdk.ErrAdmission) || next != "cursor" {
			t.Fatal(next, err)
		}
		if m, err := invalid.StartSkillRegression(ctx, time.Second, valid); !errors.Is(err, sdk.ErrAdmission) || m != nil {
			t.Fatal(m, err)
		}
	}
	for _, m := range []*sdk.SkillRegressionMonitor{nil, {}} {
		if err := m.Close(); err != nil {
			t.Fatal(err)
		}
		if m.Health().Component != "skill_regression" {
			t.Fatal(m.Health())
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	for _, input := range []context.Context{nil, canceled} {
		if _, err := client.SkillRegressionStep(input, "", valid); err == nil {
			t.Fatal("invalid ctx step accepted")
		}
		if m, err := client.StartSkillRegression(input, time.Second, valid); err == nil || m != nil {
			t.Fatal("invalid ctx monitor accepted")
		}
	}
	for _, interval := range []time.Duration{0, time.Millisecond, 25 * time.Hour} {
		if m, err := client.StartSkillRegression(ctx, interval, valid); err == nil || m != nil {
			t.Fatal("invalid interval accepted")
		}
	}
	var typednil skills.ValidatorFunc
	for _, validator := range []skills.Validator{nil, typednil} {
		if m, err := client.StartSkillRegression(ctx, time.Second, validator); err == nil || m != nil {
			t.Fatal("nil validator accepted")
		}
		if _, err := client.SkillRegressionStep(ctx, "", validator); err == nil {
			t.Fatal("nil step validator accepted")
		}
	}
	options.Overrides["skills.rollback_on_regression"] = "false"
	disabled, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	if m, err := disabled.StartSkillRegression(ctx, time.Second, valid); err == nil || m != nil {
		t.Fatal("disabled rollback accepted")
	}
	if _, err := disabled.SkillRegressionStep(ctx, "", valid); err == nil {
		t.Fatal("disabled rollback step accepted")
	}
}
