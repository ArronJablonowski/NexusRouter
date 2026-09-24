package app

import (
	"context"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/routing"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func TestRoutingBenchmarkPriorAndDirectFeedbackPrecedence(t *testing.T) {
	ctx := context.Background()
	svc, cfg := autoFixture(t)
	svc.settings.Routing.MinSamples = 1
	// This deployment prioritizes correctness. Balanced weights may knowingly
	// trade a failed answer for latency/cost; that is not evidence precedence.
	svc.settings.Routing.Weights = map[string]float64{"quality": .65, "schema_compliance": .10, "reliability": .15, "latency": .05, "cost": .03, "recency": .01, "uncertainty": .01}
	svc.settings.Routing.EvidenceFallbacks = []config.EvidenceFallback{{Domain: "code", Profile: "default", SourceDomain: "coding", SourceProfile: "benchmark"}}
	seed, err := svc.Run(ctx, Request{ModelID: "z", Prompt: "seed", Domain: "coding", Profile: "benchmark"})
	if err != nil {
		t.Fatal(err)
	}
	if err = RecordFeedback(ctx, cfg.Telemetry.Database, seed.TaskID, true, 0); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Run(ctx, Request{Prompt: "implement", Domain: "code"})
	if err != nil || got.Text != "z" {
		t.Fatalf("prior not used: %+v %v", got, err)
	}
	db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	events, err := db.Read(ctx, got.TaskID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Kind == runtime.RouteSelected {
			r := event.Data.Route.Primary
			if r.SourceDomain != "coding" || r.SourceProfile != "benchmark" || r.Confidence > .25 || r.Samples != 1 {
				t.Fatalf("unauditable prior %+v", r)
			}
		}
	}
	if _, err = db.RouteExplanation(ctx, got.TaskID); err != nil {
		t.Fatal(err)
	}
	if err = RecordFeedback(ctx, cfg.Telemetry.Database, got.TaskID, false, 0); err != nil {
		t.Fatal(err)
	}
	after, err := svc.Run(ctx, Request{Prompt: "implement", Domain: "code"})
	if err != nil || after.Text != "a" {
		t.Fatalf("prior overrode direct failure: %+v %v", after, err)
	}
	unrelated, err := svc.Run(ctx, Request{Prompt: "solve", Domain: "math"})
	if err != nil || unrelated.Text != "a" {
		t.Fatalf("prior leaked: %+v %v", unrelated, err)
	}
}

func TestRoutingBenchmarkPriorExcludesJudgeOnlyVerdicts(t *testing.T) {
	ctx := context.Background()
	svc, cfg := autoFixture(t)
	svc.settings.Routing.MinSamples = 1
	svc.settings.Routing.EvidenceFallbacks = []config.EvidenceFallback{{Domain: "code", Profile: "default", SourceDomain: "coding", SourceProfile: "benchmark"}}
	seed, err := svc.Run(ctx, Request{ModelID: "z", Prompt: "seed", Domain: "coding", Profile: "benchmark"})
	if err != nil {
		t.Fatal(err)
	}
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	events, err := db.Read(ctx, seed.TaskID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	attempt := ""
	for _, event := range events {
		if event.Kind == runtime.TurnStarted {
			attempt = event.AttemptID
		}
	}
	judge := evaluation.Record{Version: 1, ID: "judge", TaskID: seed.TaskID, AttemptID: attempt,
		Key:        routing.Key{Model: "z", Provider: "local", Domain: "coding", Profile: "benchmark"},
		Checks:     []evaluation.Check{{Source: evaluation.LLMJudge, Reference: "review", Passed: true}},
		AllowJudge: true, ExecutionSucceeded: true, Time: time.Now().UTC()}
	if err = db.RecordEvaluation(ctx, judge); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Run(ctx, Request{Prompt: "implement", Domain: "code"})
	if err != nil || got.Text != "a" {
		t.Fatalf("judge-only verdict transferred as direct prior: %+v %v", got, err)
	}
	corrected := judge
	corrected.ID = "user-correction"
	corrected.AllowJudge = false
	corrected.Checks = []evaluation.Check{{Source: evaluation.UserFeedback, Reference: "operator", Passed: true}}
	if err = db.SupersedeEvaluation(ctx, judge.ID, corrected); err != nil {
		t.Fatal(err)
	}
	got, err = svc.Run(ctx, Request{Prompt: "implement", Domain: "code"})
	if err != nil || got.Text != "z" {
		t.Fatalf("direct correction was not transferred: %+v %v", got, err)
	}
}
