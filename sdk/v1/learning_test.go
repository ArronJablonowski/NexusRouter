package v1_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	sdk "github.com/ArronJablonowski/DarwinRouter/sdk/v1"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

func TestSDKLearningStateReadOnlyAndScoped(t *testing.T) {
	options, database := sdkToolOptions(t)
	root := filepath.Join(t.TempDir(), "unopened")
	options.Overrides = map[string]string{"skills.enabled": "false", "skills.auto_draft": "false", "skills.root": root, "skills.scope": "project", "skills.learning.name": "learner"}
	client, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := client.SkillLearningState(ctx); err == nil {
		t.Fatal("missing state invented")
	}
	if _, err := os.Stat(database); !os.IsNotExist(err) {
		t.Fatal("inspection initialized database", err)
	}
	db, err := telemetry.Open(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	state := skills.LearningState{Version: 1, Scope: "project", Name: "learner", Domain: "code", PolicyDigest: strings.Repeat("a", 64), Revision: 1, Phase: "discover"}
	if err := db.PutLearningState(ctx, state, 0); err != nil {
		t.Fatal(err)
	}
	db.Close()
	got, err := client.SkillLearningState(ctx)
	if err != nil || got != state {
		t.Fatal(got, err)
	}
	options.Overrides["skills.scope"] = "other"
	other, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.SkillLearningState(ctx); err == nil {
		t.Fatal("cross-scope inspection")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("inspection initialized skill root", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := client.SkillLearningState(canceled); err == nil {
		t.Fatal("canceled inspection accepted")
	}
	for _, invalid := range []*sdk.Client{nil, {}} {
		if _, err := invalid.SkillLearningState(ctx); !errors.Is(err, sdk.ErrAdmission) {
			t.Fatal("invalid SDK accepted", err)
		}
	}
}
