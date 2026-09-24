package app

import (
	"context"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/health"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/routing"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func TestBrowserModelsClearsContextWhenNoSafeTierRemains(t *testing.T) {
	svc, cfg := autoFixture(t)
	svc.settings.Routing.EvidenceFallbacks = []config.EvidenceFallback{{Domain: "code", Profile: "default", SourceDomain: "coding", SourceProfile: "benchmark"}}
	for i := range svc.settings.Models {
		svc.settings.Models[i].ContextTokens = 131072
	}
	ctx := context.Background()
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	base := time.Now().UTC().Add(-time.Minute)
	for i, kind := range []runtime.Kind{runtime.TaskStarted, runtime.TurnStarted, runtime.TaskFailed} {
		event := inspectionEvent("context-fault", int64(i+1), kind, base)
		if i > 0 {
			event.TurnID, event.AttemptID = "turn", "attempt"
			event.Data.ModelID, event.Data.ProviderID = "a", "local"
		}
		appendInspectionEvent(t, db, event)
	}
	if err = db.RecordEvaluation(ctx, evaluation.Record{Version: 1, ID: "context-fault-eval", TaskID: "context-fault", AttemptID: "attempt", Key: routing.Key{Model: "a", Provider: "local", Domain: "code", Profile: "default"}, ContextTokens: 32768, TimedOut: true, Checks: []evaluation.Check{{Source: evaluation.Deterministic, Reference: "timeout", Passed: false}}, Time: base}); err != nil {
		t.Fatal(err)
	}
	page, err := svc.BrowserModels(ctx, health.Report{})
	if err != nil || page.Validate() != nil {
		t.Fatal(page, err)
	}
	if len(page.EvidenceFallbacks) != 1 || page.EvidenceFallbacks[0].Domain != "code" || page.EvidenceFallbacks[0].SourceProfile != "benchmark" {
		t.Fatal("configured fallback provenance was not projected", page.EvidenceFallbacks)
	}
	for _, model := range page.Models {
		switch model.ID {
		case "a":
			if model.SelectedContextTokens != nil || model.ContextSelectionStatus != "blocked" {
				t.Fatal("unsafe context was advertised as selected", model)
			}
		case "z":
			if model.SelectedContextTokens == nil || *model.SelectedContextTokens != 32768 || model.ContextSelectionStatus != "selected" {
				t.Fatal("unaffected model lost its safe baseline", model)
			}
		}
	}
}

func TestBrowserModelsDoesNotInventContextWhenEvidenceUnavailable(t *testing.T) {
	svc, _ := autoFixture(t) // No telemetry database exists yet.
	page, err := svc.BrowserModels(context.Background(), health.Report{})
	if err != nil || page.Validate() != nil {
		t.Fatal(page, err)
	}
	for _, model := range page.Models {
		if model.Configured && (model.SelectedContextTokens != nil || model.ContextSelectionStatus != "unavailable") {
			t.Fatal("missing evidence read advertised an allocation", model)
		}
	}
}

func TestBrowserFallbackFitnessRequiresEntirelyDirectEvidence(t *testing.T) {
	svc, cfg := autoFixture(t)
	svc.settings.Routing.EvidenceFallbacks = []config.EvidenceFallback{{Domain: "code", Profile: "default", SourceDomain: "coding", SourceProfile: "benchmark"}}
	ctx := context.Background()
	out, err := svc.Run(ctx, Request{ModelID: "a", Domain: "coding", Profile: "benchmark", Prompt: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = RecordFeedbackStore(ctx, db, out.TaskID, true, 0); err != nil {
		t.Fatal(err)
	}
	check := func(want bool) {
		t.Helper()
		page, err := svc.BrowserModels(ctx, health.Report{})
		if err != nil || len(page.Fitness) != 1 || page.Fitness[0].FallbackEligible != want {
			t.Fatal("fallback provenance mismatch", page.Fitness, err)
		}
	}
	check(true)
	judged, err := svc.Run(ctx, Request{ModelID: "a", Domain: "coding", Profile: "benchmark", Prompt: "another"})
	if err != nil {
		t.Fatal(err)
	}
	events, err := db.Read(ctx, judged.TaskID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var completed runtime.Event
	for _, event := range events {
		if event.Kind == runtime.TurnCompleted {
			completed = event
		}
	}
	if err = db.RecordEvaluation(ctx, evaluation.Record{Version: 1, ID: "judge-source", TaskID: judged.TaskID, AttemptID: completed.AttemptID, Key: routing.Key{Model: "a", Provider: "local", Domain: "coding", Profile: "benchmark"}, Checks: []evaluation.Check{{Source: evaluation.LLMJudge, Reference: "judge", Passed: true}}, AllowJudge: true, ExecutionSucceeded: true, Time: completed.Time}); err != nil {
		t.Fatal(err)
	}
	check(false)
	if err = ReviseFeedbackStore(ctx, db, judged.TaskID, "judge-source", true); err != nil {
		t.Fatal(err)
	}
	check(true) // A current user correction restores direct provenance once.
}
