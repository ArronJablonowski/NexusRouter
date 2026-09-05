package v1_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	sdk "github.com/ArronJablonowski/DarwinRouter/sdk/v1"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

func TestSDKSkillRegressionForwardingRestartAndCurrentState(t *testing.T) {
	options, _ := sdkToolOptions(t)
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "catalog")
	options.Overrides = map[string]string{"skills.enabled": "true", "skills.rollback_on_regression": "true", "skills.auto_activate_after_validation": "false", "skills.root": root, "skills.scope": "project"}
	store, err := skills.Open(root, []string{"project"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	key := skills.Key{Scope: "project", Name: "workflow"}
	draft := skills.Draft{Key: key, Description: "Workflow", SourceSessions: []string{"session"}, Steps: []string{"Inspect requirements"}, ValidationCases: []string{"Fixture"}}
	pass := skills.ValidatorFunc(func(context.Context, skills.Version) (skills.Evidence, error) {
		return skills.Evidence{ID: "check", Passed: true, Deterministic: true}, nil
	})
	first, err := store.Draft(ctx, draft, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Activate(ctx, key, first.ID, "", pass, false); err != nil {
		t.Fatal(err)
	}
	second, err := store.Draft(ctx, draft, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Activate(ctx, key, second.ID, first.ID, pass, false); err != nil {
		t.Fatal(err)
	}
	client, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	state, err := client.SkillActivationState(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	validator := skills.ValidatorFunc(func(_ context.Context, v skills.Version) (skills.Evidence, error) {
		calls++
		if v.ID != second.ID {
			t.Error("SDK selected wrong version")
		}
		return skills.Evidence{ID: "regression", Passed: false, Deterministic: true}, nil
	})
	result, err := client.RevalidateSkillVersion(ctx, state, validator)
	if err != nil || !result.RolledBack || result.State != state || calls != 1 {
		t.Fatal(result, err, calls)
	}
	restarted, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	current, err := restarted.SkillActivationState(ctx, key)
	if err != nil || current.Active != first.ID || current.Revision == state.Revision {
		t.Fatal(current, err)
	}
	if _, err := restarted.RevalidateSkillVersion(ctx, state, validator); err == nil || calls != 1 {
		t.Fatal("stale SDK state invoked validator", err, calls)
	}
	if _, err := restarted.RevalidateSkillVersion(nil, current, validator); !errors.Is(err, sdk.ErrAdmission) {
		t.Fatal(err)
	}
	for _, invalid := range []*sdk.Client{nil, {}} {
		if _, err := invalid.RevalidateSkillVersion(ctx, current, validator); !errors.Is(err, sdk.ErrAdmission) {
			t.Fatal(err)
		}
	}
}
