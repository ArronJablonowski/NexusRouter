package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

func TestCompactedContinuationFitsAdmissionAndSurvivesRestart(t *testing.T) {
	for _, model := range []string{"a", "auto"} {
		t.Run(model, func(t *testing.T) {
			ctx := context.Background()
			svc, cfg := autoFixture(t)
			cfg.Models[0].ContextTokens = 32768
			old := strings.Repeat("old detail ", 1200)
			first, err := RunExplicit(ctx, cfg, Request{ModelID: "a", Messages: []providers.Message{{Role: "system", Content: "Stable policy"}, {Role: "user", Content: old}}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := svc.Run(ctx, Request{ModelID: "auto", ContinueTaskID: first.TaskID, Prompt: "continue"}); err == nil {
				t.Fatal("uncompacted source unexpectedly fit context")
			}
			svc.secret = func(key string) string {
				if key == "DARWIN_API_TOKEN" {
					return "private-summary-token"
				}
				return ""
			}
			request := Request{ModelID: model, ContinueTaskID: first.TaskID, Prompt: "next question", Compaction: &sessions.CompactionRequest{Keep: 1, Summary: sessions.Summary{Decisions: []string{"Retain private-summary-token decision"}, PendingWork: []string{"Finish task"}, Requirements: []string{"Do not disclose private-summary-token"}, Activity: []string{"Inspected private-summary-token fixture"}}}}
			out, err := svc.Run(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			// Open a fresh handle: verification does not depend on service memory.
			db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			original, err := sessions.Replay(ctx, db, first.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			replayed, err := sessions.Replay(ctx, db, out.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			if original.Messages[1].Content != old || original.Compaction != nil || replayed.Compaction == nil || replayed.Compaction.SourceTaskID != first.TaskID || replayed.Compaction.SourceSequence != original.Sequence || replayed.Compaction.RemovedMessages != 1 || replayed.SessionID != original.SessionID || replayed.Privacy != "local_only" {
				t.Fatal("lost compaction provenance or original history", replayed)
			}
			encoded, _ := json.Marshal(replayed)
			if strings.Contains(string(encoded), "private-summary-token") || strings.Contains(string(encoded), old) || !strings.Contains(string(encoded), "[REDACTED]") || replayed.Messages[0].Content != "Stable policy" {
				t.Fatal("invalid compacted/redacted context", string(encoded))
			}
			if request.Compaction.Summary.Decisions[0] != "Retain private-summary-token decision" {
				t.Fatal("mutated caller summary")
			}
			if replayed.Compaction.Summary.Requirements[0] != "Do not disclose [REDACTED]" || replayed.Compaction.Summary.Activity[0] != "Inspected [REDACTED] fixture" || !strings.Contains(request.Compaction.Summary.Requirements[0], "private-summary-token") || !strings.Contains(request.Compaction.Summary.Activity[0], "private-summary-token") {
				t.Fatal("new summary fields were not redacted independently")
			}
			// A later ordinary continuation must use the persisted compacted input.
			next, err := svc.Run(ctx, Request{ModelID: model, ContinueTaskID: out.TaskID, Prompt: "one more"})
			if err != nil || next.TaskID == "" {
				t.Fatal(next, err)
			}
		})
	}
}

func TestCompactionAdmissionRequiresSourceAndRetainsPrivacy(t *testing.T) {
	ctx := context.Background()
	svc, cfg := autoFixture(t)
	compact := &sessions.CompactionRequest{Keep: 1, Summary: sessions.Summary{Decisions: []string{"retain decision"}}}
	if _, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "no parent", Compaction: compact}); err != ErrAdmission {
		t.Fatal(err)
	}
	first, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "private history"})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Mode = "hybrid"
	cfg.Models[0].Locality = "cloud"
	if _, err := RunExplicit(ctx, cfg, Request{ModelID: "a", Prompt: "offload", ContinueTaskID: first.TaskID, Compaction: compact}, nil); err != ErrAdmission {
		t.Fatal("compaction laundered private history", err)
	}
	compact.Keep = 100
	if _, err := svc.Run(ctx, Request{ModelID: "auto", Prompt: "no-op", ContinueTaskID: first.TaskID, Compaction: compact}); err != ErrAdmission {
		t.Fatal("no-op compaction accepted", err)
	}
}
