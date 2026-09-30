package v1_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
)

func TestSDKSkillTaskOutcome(t *testing.T) {
	ctx := context.Background()
	for _, client := range []*sdk.Client{nil, {}} {
		if out, err := client.SkillTaskOutcome(ctx, "task"); err == nil || out.TaskID != "" {
			t.Fatal("invalid client accepted")
		}
	}
	options, path := sdkToolOptions(t)
	root := filepath.Join(t.TempDir(), "unopened-skills")
	options.Overrides = map[string]string{"skills.scope": "project", "skills.root": root, "skills.enabled": "false"}
	c, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := c.SkillTaskOutcome(ctx, "task"); err == nil || out.TaskID != "" {
		t.Fatal("missing task accepted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("inspection created database")
	}
	db, err := telemetry.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i, kind := range []runtime.Kind{runtime.TaskStarted, runtime.TaskCanceled} {
		e := runtime.Event{Version: 1, ID: string(kind), TaskID: "task", SessionID: "session", CorrelationID: "task", Sequence: int64(i + 1), Kind: kind, Time: time.Now().UTC()}
		if i == 0 {
			e.Data.SkillContext = &runtime.SkillContextUse{Version: 1, Complete: true}
		}
		if err = db.Append(ctx, int64(i), e); err != nil {
			t.Fatal(err)
		}
	}
	out, err := c.SkillTaskOutcome(ctx, "task")
	if err != nil || out.Validate() != nil || out.State != "canceled" || out.SkillContext == nil || !out.SkillContext.Complete || len(out.SkillContext.References) != 0 || out.Quality != nil {
		t.Fatal(out, err)
	}
	out.SkillContext.Complete = false
	again, err := c.SkillTaskOutcome(ctx, "task")
	if err != nil || !again.SkillContext.Complete {
		t.Fatal("returned alias", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	for _, input := range []context.Context{nil, canceled} {
		if out, err := c.SkillTaskOutcome(input, "task"); err == nil || out.TaskID != "" {
			t.Fatal("canceled inspection accepted")
		}
	}
	if out, err := c.SkillTaskOutcome(ctx, "bad:id"); err == nil || out.TaskID != "" {
		t.Fatal("invalid task accepted")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("inspection opened catalog")
	}
}
