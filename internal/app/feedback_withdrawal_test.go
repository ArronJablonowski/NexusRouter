package app

import (
	"context"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
)

func TestFeedbackWithdrawalStoreAndRestore(t *testing.T) {
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
	if err := RecordFeedbackStore(ctx, db, out.TaskID, false, 0); err != nil {
		t.Fatal(err)
	}
	history, err := FeedbackHistoryStore(ctx, db, out.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	base := history[0]
	for i := 0; i < 2; i++ {
		if err := WithdrawFeedbackStore(ctx, db, out.TaskID, base.ID); err != nil {
			t.Fatal(err)
		}
	}
	history, err = FeedbackHistoryStore(ctx, db, out.TaskID)
	if err != nil || len(history) != 2 || history[0].Checks[0].Passed || history[1].Checks[0].Source != evaluation.Withdrawn {
		t.Fatal(history, err)
	}
	set, err := db.ObservationSet(ctx, base.Key)
	if err != nil || len(set.Fitness) != 0 {
		t.Fatal(set, err)
	}
	if err := ReviseFeedbackStore(ctx, db, out.TaskID, history[1].ID, true); err != nil {
		t.Fatal(err)
	}
	fitness, err := db.Fitness(ctx, base.Key)
	if err != nil || fitness.Samples != 1 || fitness.Quality != 1 {
		t.Fatal(fitness, err)
	}
}
