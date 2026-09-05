package app

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func TestContextEngineSummarySelectionFrozenThroughApproval(t *testing.T) {
	ctx := context.Background()
	svc, cfg := autoFixture(t)
	first, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "old private-secret decision"})
	if err != nil {
		t.Fatal(err)
	}
	source, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "recent requirement", ContinueTaskID: first.TaskID})
	if err != nil {
		t.Fatal(err)
	}
	db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	before, err := sessions.Replay(ctx, db, source.TaskID)
	if err != nil || len(before.Messages) != 4 {
		t.Fatal(before, err)
	}
	var selections, calls atomic.Int32
	engine := applicationContextEngine{compact: func(_ context.Context, s sessions.Snapshot, r sessions.CompactionRequest) (sessions.CompactionRequest, error) {
		selections.Add(1)
		encoded, _ := json.Marshal(s)
		if r.Keep != 1 || strings.Contains(string(encoded), "private-secret") || !strings.Contains(string(encoded), "[REDACTED]") {
			t.Error("wrong or unsanitized planner input")
		}
		s.Messages[0].Content = "MUTATED SNAPSHOT"
		r.Keep = 3
		return r, nil
	}}
	svc.contextEngine, svc.contextEstimator = engine, engine
	svc.secret = func(string) string { return "private-secret" }
	svc.providerFactory = applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		return delegateEstimatorProvider(func(_ context.Context, r providers.Request, emit func(providers.Chunk) error) error {
			text := "continued"
			if calls.Add(1) == 1 {
				text = `{"version":1,"summary":{"decisions":["Preserve decision"]}}`
			}
			return emit(providers.Chunk{Text: text, Done: true, FinishReason: "stop"})
		}), nil
	})
	attempt, err := svc.SummarizeTask(ctx, source.TaskID, "a", 1, 0)
	if err != nil || attempt.Draft == nil || attempt.Keep != 3 || attempt.Draft.Request.Keep != 3 || selections.Load() != 1 {
		t.Fatal(attempt, err, selections.Load())
	}
	_, canonical, err := sessions.PrepareContinuation(before, attempt.Draft.Request)
	if err != nil || !reflect.DeepEqual(canonical, attempt.Draft.Checkpoint) {
		t.Fatal("source provenance differs from original history", err)
	}
	request := Request{ModelID: "auto", ContinueTaskID: source.TaskID, SummaryAttemptID: attempt.ID, Prompt: "continue"}
	if _, err := svc.Run(ctx, request); err == nil || calls.Load() != 1 {
		t.Fatal("unreviewed summary used", err, calls.Load())
	}
	approved, err := svc.ReviewSummary(ctx, attempt.ID, "", "approved", "Validated against source")
	if err != nil {
		t.Fatal(err)
	}
	// Replacing the engine cannot reinterpret a saved, approved selection.
	engine.compact = func(context.Context, sessions.Snapshot, sessions.CompactionRequest) (sessions.CompactionRequest, error) {
		t.Error("approved compaction was replanned")
		panic("must not execute")
	}
	svc.contextEngine, svc.contextEstimator = engine, engine
	out, err := svc.Run(ctx, request)
	if err != nil || out.Text != "continued" || calls.Load() != 2 {
		t.Fatal(out, err, calls.Load())
	}
	continued, err := sessions.Replay(ctx, db, out.TaskID)
	if err != nil || continued.Compaction == nil || continued.Compaction.SummaryReviewID != approved.ID {
		t.Fatal("missing approval checkpoint", err)
	}
	checkpoint := *continued.Compaction
	checkpoint.SummaryAttemptID, checkpoint.SummaryReviewID = "", ""
	if !reflect.DeepEqual(&checkpoint, canonical) {
		t.Fatal("approved source or retention changed")
	}
	after, err := sessions.Replay(ctx, db, source.TaskID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("source history mutated", err)
	}
}
