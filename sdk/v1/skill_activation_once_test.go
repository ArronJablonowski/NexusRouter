package v1_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
	"github.com/ArronJablonowski/NexusRouter/skills"
)

func TestSDKSkillActivationOnceForwarding(t *testing.T) {
	options, _ := sdkToolOptions(t)
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "catalog")
	options.Overrides = map[string]string{"skills.enabled": "true", "skills.auto_activate_after_validation": "true", "skills.root": root, "skills.scope": "project"}
	store, err := skills.Open(root, []string{"project"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	v, err := store.Draft(ctx, skills.Draft{Key: skills.Key{Scope: "project", Name: "workflow"}, Description: "Reusable workflow", SourceSessions: []string{"session-a", "session-b"}, SourceEvidence: []string{"proof"}, Steps: []string{"Inspect requirements"}, ValidationCases: []string{"Fixture"}}, false)
	store.Close()
	if err != nil {
		t.Fatal(err)
	}
	client, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	state, err := client.SkillActivationState(ctx, v.Draft.Key)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	validator := skills.ValidatorFunc(func(context.Context, skills.Version) (skills.Evidence, error) {
		calls++
		return skills.Evidence{ID: "proof", Passed: true, Deterministic: true}, nil
	})
	for range 2 {
		if err := client.ActivateSkillVersionOnce(ctx, "sdk-operation", state, v.ID, validator); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatal("SDK repeated validator", calls)
	}
	receipt, err := client.SkillActivationOperation(ctx, v.Draft.Key, "sdk-operation")
	if err != nil || receipt.Expected != state || receipt.Candidate != v.ID {
		t.Fatal(receipt, err)
	}
	for _, bad := range []*sdk.Client{nil, {}} {
		if err := bad.ActivateSkillVersionOnce(ctx, "sdk-operation", state, v.ID, validator); !errors.Is(err, sdk.ErrAdmission) {
			t.Fatal(err)
		}
		if _, err := bad.SkillActivationOperation(ctx, v.Draft.Key, "sdk-operation"); !errors.Is(err, sdk.ErrAdmission) {
			t.Fatal(err)
		}
	}
	if err := client.ActivateSkillVersionOnce(nil, "sdk-operation", state, v.ID, validator); !errors.Is(err, sdk.ErrAdmission) {
		t.Fatal(err)
	}
}
