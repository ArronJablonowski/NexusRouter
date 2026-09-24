package app

import (
	"context"
	"errors"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/routing"
)

func TestFeedbackRevisionStorePreservesAtomicHistory(t *testing.T) {
	svc, cfg := autoFixture(t)
	ctx := context.Background()
	out, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "hello", Domain: "creative"})
	if err != nil {
		t.Fatal(err)
	}
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = RecordFeedbackStore(ctx, db, out.TaskID, false, 0); err != nil {
		t.Fatal(err)
	}
	history, err := FeedbackHistoryStore(ctx, db, out.TaskID)
	if err != nil || len(history) != 1 {
		t.Fatalf("history=%+v err=%v", history, err)
	}
	prior := history[0]
	results := make(chan error, 2)
	for _, accepted := range []bool{false, true} {
		go func(accepted bool) {
			results <- ReviseFeedbackStore(ctx, db, out.TaskID, prior.ID, accepted)
		}(accepted)
	}
	successes := 0
	for i := 0; i < 2; i++ {
		if err := <-results; err == nil {
			successes++
		} else if !errors.Is(err, telemetry.ErrConflict) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("competing revisions admitted %d times", successes)
	}
	history, err = FeedbackHistoryStore(ctx, db, out.TaskID)
	if err != nil || len(history) != 2 {
		t.Fatalf("history=%+v err=%v", history, err)
	}
	current := history[1]
	if err = ReviseFeedbackStore(ctx, db, out.TaskID, prior.ID, current.Checks[0].Passed); err != nil {
		t.Fatal("identical retry failed", err)
	}
	fitness, err := db.Fitness(ctx, current.Key)
	wantQuality := 0.0
	if current.Checks[0].Passed {
		wantQuality = 1
	}
	if err != nil || fitness.Samples != 1 || fitness.Quality != wantQuality {
		t.Fatalf("fitness=%+v err=%v", fitness, err)
	}
}

func TestFeedbackStoreHelpersRejectUnavailableStore(t *testing.T) {
	if _, err := FeedbackHistoryStore(context.Background(), nil, "task"); err == nil {
		t.Fatal("nil history store accepted")
	}
	if err := ReviseFeedbackStore(context.Background(), nil, "task", "prior", true); err == nil {
		t.Fatal("nil revision store accepted")
	}
}

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
