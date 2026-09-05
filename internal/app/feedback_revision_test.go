package app

import (
	"context"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/routing"
)

func TestFeedbackRevisionKeepsOneAttemptContribution(t *testing.T) {
	svc, cfg := autoFixture(t)
	ctx := context.Background()
	out, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "hello", Domain: "creative"})
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordFeedback(ctx, cfg.Telemetry.Database, out.TaskID, false, 0); err != nil {
		t.Fatal(err)
	}
	history, err := FeedbackHistory(ctx, cfg.Telemetry.Database, out.TaskID)
	if err != nil || len(history) != 1 {
		t.Fatalf("%+v %v", history, err)
	}
	prior := history[0]
	for i := 0; i < 2; i++ {
		if err := ReviseFeedback(ctx, cfg.Telemetry.Database, out.TaskID, prior.ID, true); err != nil {
			t.Fatal(err)
		}
	}
	history, err = FeedbackHistory(ctx, cfg.Telemetry.Database, out.TaskID)
	if err != nil || len(history) != 2 || history[0].Checks[0].Passed || !history[1].Checks[0].Passed {
		t.Fatalf("%+v %v", history, err)
	}
	if err := ReviseFeedback(ctx, cfg.Telemetry.Database, out.TaskID, prior.ID, false); err == nil {
		t.Fatal("stale correction accepted")
	}
	db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	fitness, err := db.Fitness(ctx, routing.Key{Model: "a", Provider: "local", Domain: "creative", Profile: "default"})
	if err != nil || fitness.Samples != 1 || fitness.Quality != 1 || fitness.Reliability != 1 {
		t.Fatalf("%+v %v", fitness, err)
	}
}
