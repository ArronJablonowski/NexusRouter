package v1_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
	"github.com/ArronJablonowski/NexusRouter/skills"
)

func TestSDKPublishSkillGenerationSavedProposal(t *testing.T) {
	options, database := sdkToolOptions(t)
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "catalog")
	options.Overrides = map[string]string{"skills.enabled": "true", "skills.auto_draft": "true", "skills.root": root, "skills.scope": "project"}
	ctx := context.Background()
	db, err := telemetry.Open(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	a := skills.GenerationAttempt{Version: 1, ID: "publication", Key: skills.Key{Scope: "project", Name: "workflow"}, Model: "fixture", Provider: "local", InputDigest: strings.Repeat("a", 64), SourceSessions: []string{"session-a", "session-b"}, SourceEvidence: []string{"proof"}, Status: "started", StartedAt: time.Now().UTC()}
	if err = db.BeginSkillGeneration(ctx, a); err != nil {
		t.Fatal(err)
	}
	a.Status, a.FinishedAt = "drafted", a.StartedAt.Add(time.Millisecond)
	a.Result = &skills.ModelDraftResult{Model: a.Model, Draft: skills.Draft{Key: a.Key, Description: "Saved workflow", Steps: []string{"Inspect input"}, ValidationCases: []string{"Check output"}, SourceSessions: a.SourceSessions, SourceEvidence: a.SourceEvidence}}
	if err = db.FinishSkillGeneration(ctx, a); err != nil {
		t.Fatal(err)
	}
	db.Close()
	client, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	v, err := client.PublishSkillGeneration(ctx, a.ID)
	if err != nil || v.ID == "" || !reflect.DeepEqual(v.Draft, a.Result.Draft) {
		t.Fatal(v, err)
	}
	restarted, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	again, err := restarted.PublishSkillGeneration(ctx, a.ID)
	if err != nil || !reflect.DeepEqual(v, again) {
		t.Fatal("repeat publication", again, err)
	}
	store, err := skills.OpenReadOnly(root, []string{"project"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	history, err := store.History(ctx, a.Key)
	if err != nil || history.Active != "" || len(history.Versions) != 1 {
		t.Fatal("publication activated or duplicated", history, err)
	}
}

func TestSDKPublishSkillGenerationClientAndContextGuards(t *testing.T) {
	for _, client := range []*sdk.Client{nil, {}} {
		got, err := client.PublishSkillGeneration(context.Background(), "publication")
		if !errors.Is(err, sdk.ErrAdmission) || !reflect.DeepEqual(got, skills.Version{}) {
			t.Fatal("invalid client accepted")
		}
	}
	options, database := sdkToolOptions(t)
	root := filepath.Join(t.TempDir(), "catalog")
	options.Overrides = map[string]string{"skills.enabled": "true", "skills.auto_draft": "true", "skills.root": root, "skills.scope": "project"}
	client, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, ctx := range []context.Context{nil, ctx, context.Background()} {
		got, err := client.PublishSkillGeneration(ctx, "missing")
		if err == nil || !reflect.DeepEqual(got, skills.Version{}) {
			t.Fatal("invalid publication accepted")
		}
	}
	for _, path := range []string{root, database} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("publication initialized missing storage", path, err)
		}
	}
}
