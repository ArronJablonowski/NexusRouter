package v1_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	sdk "github.com/ArronJablonowski/DarwinRouter/sdk/v1"
)

func TestSDKSkillComparisonSelectionReadOnly(t *testing.T) {
	ctx := context.Background()
	input := sdk.SkillComparisonSelectionRequest{Version: 1, ModelID: "chat", Domain: "creative", Profile: "default", Name: "workflow", BaselineVersion: strings.Repeat("a", 32), CandidateVersion: strings.Repeat("b", 32), Source: evaluation.UserFeedback, MinSamples: 20, MinDrop: .1, Privacy: "local_only", TasksPerVersion: 20}
	for _, c := range []*sdk.Client{nil, {}} {
		if out, err := c.SelectSkillComparison(ctx, input); err == nil || out.Version != 0 {
			t.Fatal("invalid client admitted")
		}
	}
	options, path := sdkToolOptions(t)
	root := filepath.Join(t.TempDir(), "absent-skills")
	options.Overrides = map[string]string{"skills.scope": "project", "skills.root": root, "skills.enabled": "false"}
	c, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := c.SelectSkillComparison(ctx, input); err == nil || out.Version != 0 {
		t.Fatal("missing storage admitted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("database created")
	}
	db, err := telemetry.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	out, err := c.SelectSkillComparison(ctx, input)
	if err != nil || out.Validate() != nil || out.ConfiguredModelID != "chat" || out.Comparison != nil || out.Watermark != 0 {
		t.Fatal(out, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c.SelectSkillComparison(canceled, input); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := c.SelectSkillComparison(nil, input); err == nil {
		t.Fatal("nil context admitted")
	}
	input.TasksPerVersion = 19
	if out, err := c.SelectSkillComparison(ctx, input); err == nil || out.Version != 0 {
		t.Fatal("invalid selection admitted")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("inspection mutated database")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("catalog created")
	}
}
