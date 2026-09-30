package app

import (
	"context"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

func TestExplicitCompactionUsesAllocatedWindow(t *testing.T) {
	ctx := context.Background()
	svc, cfg := autoFixture(t)
	source, err := svc.Run(ctx, Request{ModelID: "a", Prompt: strings.Repeat("history ", 400)})
	if err != nil {
		t.Fatal(err)
	}
	attempt, _ := approvedOverflowSummary(t, svc, cfg, source.TaskID)
	svc.settings.Runtime.AutoApprovedCompaction = true
	svc.settings.Models[0].DefaultContextTokens = 2048
	result, err := svc.Run(ctx, Request{ModelID: "a", ContinueTaskID: source.TaskID, Prompt: "continue"})
	if err != nil || result.Text != "a" {
		t.Fatal("allocated continuation failed", result, err)
	}
	db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	replayed, err := sessions.Replay(ctx, db, result.TaskID)
	if err != nil || replayed.Compaction == nil || replayed.Compaction.SummaryAttemptID != attempt.ID {
		t.Fatal("continuation did not compact before first dispatch", replayed, err)
	}
	events, err := db.Read(ctx, result.TaskID, 0, 100)
	if err != nil || len(events) == 0 || events[0].Data.ContextTokens != 2048 {
		t.Fatal("allocation was not preserved", events, err)
	}
}
