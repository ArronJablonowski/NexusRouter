package v1_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
	"github.com/ArronJablonowski/NexusRouter/skills"
)

func TestSDKOutcomeRollbackIntentReadOnlyGuards(t *testing.T) {
	options, path := sdkToolOptions(t)
	root := filepath.Join(t.TempDir(), "absent-catalog")
	options.Overrides = map[string]string{"skills.root": root, "skills.scope": "project", "skills.enabled": "false"}
	c, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, client := range []*sdk.Client{nil, {}, c} {
		for _, input := range []context.Context{nil, context.Background(), ctx} {
			if out, err := client.OutcomeRollbackIntent(input, skills.Key{Scope: "project", Name: "workflow"}, "operation"); err == nil || out.Version != 0 {
				t.Fatal("missing intent admitted")
			}
		}
	}
	if _, err := c.OutcomeRollbackIntent(ctx, skills.Key{Scope: "project", Name: "workflow"}, "operation"); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost", err)
	}
	for _, key := range []skills.Key{{Scope: "other", Name: "workflow"}, {Scope: "project", Name: "invalid/name"}} {
		if out, err := c.OutcomeRollbackIntent(context.Background(), key, "operation"); err == nil || out.Version != 0 {
			t.Fatal("invalid scope/key admitted")
		}
	}
	for _, missing := range []string{path, root} {
		if _, err := os.Stat(missing); !os.IsNotExist(err) {
			t.Fatal("inspection created storage", err)
		}
	}
}

func TestSDKOutcomeRollbackInterruptedSelectionIntent(t *testing.T) {
	ctx := context.Background()
	options, database := sdkToolOptions(t)
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "catalog")
	options.Overrides = map[string]string{"skills.root": root, "skills.scope": "project", "skills.outcome_rollback": "true"}
	store, err := skills.Open(root, []string{"project"})
	if err != nil {
		t.Fatal(err)
	}
	key := skills.Key{Scope: "project", Name: "workflow"}
	draft := skills.Draft{Key: key, Description: "workflow", Tags: []string{"creative"}, SourceSessions: []string{"fixture"}, Steps: []string{"Inspect input"}, ValidationCases: []string{"fixture"}}
	validator := skills.ValidatorFunc(func(context.Context, skills.Version) (skills.Evidence, error) {
		return skills.Evidence{ID: "trusted-fixture", Passed: true, Deterministic: true}, nil
	})
	previous := ""
	versions := []string{}
	for i := 0; i < 2; i++ {
		v, err := store.Draft(ctx, draft, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Activate(ctx, key, v.ID, previous, validator, false); err != nil {
			t.Fatal(err)
		}
		versions = append(versions, v.ID)
		previous = v.ID
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	c, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := c.SkillActivationState(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	request := skills.ComparisonSelectionRequest{Version: 1, ModelID: "chat", Domain: "creative", Profile: "default", Name: key.Name, BaselineVersion: versions[0], CandidateVersion: versions[1], Source: evaluation.UserFeedback, MinSamples: 20, MinDrop: .1, Privacy: "local_only", TasksPerVersion: 20}
	// Missing telemetry makes the first read-only selector fail after its claim.
	if out, err := c.OutcomeRollbackOnce(ctx, "failed-selector", expected, request); err == nil || out.Version != 0 {
		t.Fatal("missing selection evidence accepted")
	}
	intent, err := c.OutcomeRollbackIntent(ctx, key, "failed-selector")
	if err != nil || intent.Validate() != nil || intent.Expected != expected || intent.ConfiguredModelID != "chat" {
		t.Fatal("failed selector lost durable claim", err)
	}
	before, err := os.ReadFile(filepath.Join(root, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	if out, err := c.OutcomeRollbackOnce(ctx, "failed-selector", expected, request); err == nil || out.Version != 0 {
		t.Fatal("failed selector resumed")
	}
	if _, err := c.OutcomeRollbackOperation(ctx, key, "failed-selector"); err == nil {
		t.Fatal("failure invented a receipt")
	}
	after, err := os.ReadFile(filepath.Join(root, "catalog.json"))
	if err != nil || string(before) != string(after) {
		t.Fatal("inspection/retry mutated claim", err)
	}
	current, err := c.SkillActivationState(ctx, key)
	if err != nil || current != expected {
		t.Fatal("failed selection changed activation", err)
	}
	if _, err := os.Stat(database); !os.IsNotExist(err) {
		t.Fatal("failed selector created database", err)
	}
}
