package v1_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	sdk "github.com/ArronJablonowski/DarwinRouter/sdk/v1"
)

func TestSDKSkillComparisonReadOnly(t *testing.T) {
	ctx := context.Background()
	request := sdk.SkillComparisonRequest{Version: 1, ModelID: "chat", Domain: "creative", Profile: "default", Name: "workflow", BaselineVersion: strings.Repeat("a", 32), CandidateVersion: strings.Repeat("b", 32), Source: evaluation.UserFeedback, MinSamples: 20, MinDrop: 0.1, Tasks: []string{"task"}}
	for _, c := range []*sdk.Client{nil, {}} {
		if out, err := c.CompareSkillOutcomes(ctx, request); err == nil || out.Version != 0 {
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
	if out, err := c.CompareSkillOutcomes(ctx, request); err == nil || out.Version != 0 {
		t.Fatal("missing storage admitted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("created missing database", err)
	}
	db, err := telemetry.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	for i, kind := range []runtime.Kind{runtime.TaskStarted, runtime.TaskCanceled} {
		e := runtime.Event{Version: 1, ID: string(kind), TaskID: "task", SessionID: "session", CorrelationID: "task", Sequence: int64(i + 1), Time: time.Now().UTC(), Kind: kind}
		if err := db.Append(ctx, int64(i), e); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.CompareSkillOutcomes(ctx, request)
	if err != nil || out.Validate() != nil || !out.AdvisoryOnly || out.Sampled != 1 || out.Excluded["nonfinal_outcome"] != 1 {
		t.Fatal("unexpected report", out, err)
	}
	out.Excluded["nonfinal_outcome"] = 99
	again, err := c.CompareSkillOutcomes(ctx, request)
	if err != nil || again.Excluded["nonfinal_outcome"] != 1 {
		t.Fatal("returned alias", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c.CompareSkillOutcomes(canceled, request); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost", err)
	}
	if _, err := c.CompareSkillOutcomes(nil, request); err == nil {
		t.Fatal("nil context admitted")
	}
	request.Tasks = append(request.Tasks, "task")
	if out, err := c.CompareSkillOutcomes(ctx, request); err == nil || out.Version != 0 {
		t.Fatal("duplicate task admitted")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("inspection mutated database", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("inspection created catalog", err)
	}
}
