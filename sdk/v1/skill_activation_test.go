package v1_test

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
	"github.com/ArronJablonowski/NexusRouter/skills"
)

func TestSDKSkillActivationForwardingAndRestart(t *testing.T) {
	options, database := sdkToolOptions(t)
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "catalog")
	options.Overrides = map[string]string{"skills.enabled": "true", "skills.auto_draft": "true", "skills.auto_activate_after_validation": "true", "skills.root": root, "skills.scope": "project"}
	ctx := context.Background()
	db, err := telemetry.Open(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	a := skills.GenerationAttempt{Version: 1, ID: "generation", Key: skills.Key{Scope: "project", Name: "workflow"}, Model: "fixture", Provider: "local", InputDigest: strings.Repeat("a", 64), SourceSessions: []string{"session-a", "session-b"}, SourceEvidence: []string{"proof"}, Status: "started", StartedAt: time.Now().UTC()}
	if err := db.BeginSkillGeneration(ctx, a); err != nil {
		t.Fatal(err)
	}
	a.Status = "drafted"
	a.FinishedAt = a.StartedAt.Add(time.Second)
	a.Result = &skills.ModelDraftResult{Model: a.Model, Draft: skills.Draft{Key: a.Key, Description: "Reusable workflow", SourceSessions: a.SourceSessions, SourceEvidence: a.SourceEvidence, Steps: []string{"Inspect requirements"}, ValidationCases: []string{"Fixture"}}}
	if err := db.FinishSkillGeneration(ctx, a); err != nil {
		t.Fatal(err)
	}
	db.Close()
	client, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	v, err := client.PublishSkillGeneration(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := client.SkillActivationState(ctx, a.Key)
	if err != nil || state.Active != "" {
		t.Fatal(state, err)
	}
	calls := 0
	validator := skills.ValidatorFunc(func(_ context.Context, candidate skills.Version) (skills.Evidence, error) {
		calls++
		if !reflect.DeepEqual(candidate, v) {
			t.Error("SDK changed candidate")
		}
		return skills.Evidence{ID: "proof", Passed: true, Deterministic: true}, nil
	})
	if err := client.ActivateSkillVersion(ctx, state, v.ID, validator); err != nil || calls != 1 {
		t.Fatal(err, calls)
	}
	reopened, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	current, err := reopened.SkillActivationState(ctx, a.Key)
	if err != nil || current.Active != v.ID || current.Revision == state.Revision {
		t.Fatal(current, err)
	}
	if err := reopened.ActivateSkillVersion(ctx, state, v.ID, validator); err == nil || calls != 1 {
		t.Fatal("stale SDK request invoked validator", err, calls)
	}
	options.Overrides["skills.auto_activate_after_validation"] = "false"
	disabled, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := disabled.SkillActivationState(ctx, a.Key); err != nil {
		t.Fatal("read requires activation switch", err)
	}
	if err := disabled.ActivateSkillVersion(ctx, current, v.ID, validator); err == nil || calls != 1 {
		t.Fatal("disabled activation invoked validator", err, calls)
	}
}

func TestSDKSkillActivationInvalidClientsAndContexts(t *testing.T) {
	key := skills.Key{Scope: "project", Name: "workflow"}
	state := skills.ActivationState{Version: 1, Key: key, Revision: strings.Repeat("a", 64)}
	for _, client := range []*sdk.Client{nil, {}} {
		if _, err := client.SkillActivationState(context.Background(), key); !errors.Is(err, sdk.ErrAdmission) {
			t.Fatal(err)
		}
		if err := client.ActivateSkillVersion(context.Background(), state, strings.Repeat("a", 32), nil); !errors.Is(err, sdk.ErrAdmission) {
			t.Fatal(err)
		}
	}
	options, _ := sdkToolOptions(t)
	client, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.SkillActivationState(nil, key); !errors.Is(err, sdk.ErrAdmission) {
		t.Fatal(err)
	}
	if err := client.ActivateSkillVersion(nil, state, strings.Repeat("a", 32), nil); !errors.Is(err, sdk.ErrAdmission) {
		t.Fatal(err)
	}
}
