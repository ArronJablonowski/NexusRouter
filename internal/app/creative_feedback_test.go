package app

import (
	"context"
	"github.com/ArronJablonowski/NexusRouter/internal/browserops"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/routing"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	contract "github.com/ArronJablonowski/NexusRouter/webui"
	"testing"
)

func TestCreativeRatingAttributionRevisionAndOpinions(t *testing.T) {
	svc, cfg := autoFixture(t)
	ctx := context.Background()
	out, err := svc.Run(ctx, Request{Prompt: "Write a poem", Domain: "writing"})
	if err != nil {
		t.Fatal(err)
	}
	ops, err := browserops.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer ops.Close()
	bridge, err := NewBrowserMutations(svc, ops)
	if err != nil {
		t.Fatal(err)
	}
	command := contract.FeedbackRequest{Version: 1, IdempotencyKey: "creative-rating-once", TaskID: out.TaskID, Action: contract.FeedbackRecord, Rating: 9, Accepted: true}
	receipt, err := bridge.Feedback(ctx, browserMutationTestSubject, command)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := bridge.Feedback(ctx, browserMutationTestSubject, command)
	if err != nil || replay != receipt {
		t.Fatal(replay, err)
	}
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	history, err := db.BrowserFeedbackHistory(ctx, out.TaskID)
	if err != nil || len(history) != 1 || history[0].Rating != 9 || history[0].CreativeKind != "poetry" || history[0].Model == "" || history[0].CostStatus == "" {
		t.Fatal(history, err)
	}
	snapshot, err := db.TaskSnapshot(ctx, out.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Messages = []providers.Message{{Role: "user", Content: "Write a story"}, {Role: "user", Content: "Write a poem"}}
	preview := telemetry.BrowserFeedback{Accepted: true}
	if err = bridge.fillCreativeFeedback(ctx, db, snapshot, &preview, 8); err != nil || preview.CreativeKind != "poetry" {
		t.Fatal("used old conversation prompt", preview, err)
	}
	key := routing.Key{Model: history[0].Model, Provider: history[0].Provider, Domain: "writing", Profile: "default"}
	opinions, err := db.CreativeOpinions(ctx, key, "poetry")
	if err != nil || len(opinions) != 1 || opinions[0].Quality != 8.0/9 {
		t.Fatal(opinions, err)
	}
	wrong, err := db.CreativeOpinions(ctx, key, "image")
	if err != nil || len(wrong) != 0 {
		t.Fatal("cross-category preference", wrong, err)
	}
	rev := receipt.Revision
	command.Action = contract.FeedbackRevise
	command.FeedbackID = receipt.FeedbackID
	command.ExpectedRevision = &rev
	command.IdempotencyKey = "creative-rating-revise"
	command.Rating = 2
	command.Accepted = false
	if _, err = bridge.Feedback(ctx, browserMutationTestSubject, command); err != nil {
		t.Fatal(err)
	}
	opinions, err = db.CreativeOpinions(ctx, key, "poetry")
	if err != nil || len(opinions) != 1 || opinions[0].Quality != 1.0/9 {
		t.Fatal("revision inflated sample count", opinions, err)
	}
	status, err := bridge.FeedbackContext(ctx, out.TaskID)
	if err != nil || status.Rating != 2 || status.CostSummary == "" {
		t.Fatal(status, err)
	}
}

func TestCreativeCategoryBoundaries(t *testing.T) {
	for prompt, want := range map[string]string{"Write poems": "poetry", "Explain history": "general", "Generate three images": "image", "Draft an advertisement": "marketing"} {
		if got := creativeKind(prompt); got != want {
			t.Fatal(prompt, got, want)
		}
	}
}

func TestCreativeRatingsGuideAutomaticChoiceByCategory(t *testing.T) {
	svc, cfg := autoFixture(t)
	ctx := context.Background()
	ops, err := browserops.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer ops.Close()
	bridge, err := NewBrowserMutations(svc, ops)
	if err != nil {
		t.Fatal(err)
	}
	for _, seed := range []struct {
		model  string
		rating int
	}{{"a", 1}, {"z", 10}} {
		out, err := svc.Run(ctx, Request{ModelID: seed.model, Prompt: "Write a poem", Domain: "creative"})
		if err != nil {
			t.Fatal(err)
		}
		_, err = bridge.Feedback(ctx, browserMutationTestSubject, contract.FeedbackRequest{Version: 1, IdempotencyKey: "creative-rating-choice-" + seed.model, TaskID: out.TaskID, Action: contract.FeedbackRecord, Rating: seed.rating, Accepted: seed.rating >= 6})
		if err != nil {
			t.Fatal(err)
		}
	}
	out, err := svc.Run(ctx, Request{Prompt: "Write another poem", Domain: "creative"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Text != "z" {
		t.Fatal("ratings did not improve matching automatic choice", out.Text)
	}
}

func TestCreativeRatingsIncludePairedCandidates(t *testing.T) {
	svc, cfg := autoFixture(t)
	svc.ConfigureFederatedRouting(strongerFederation())
	ctx := context.Background()
	out, err := svc.Run(ctx, Request{ModelID: "auto", Messages: []providers.Message{{Role: "user", Content: "Write a poem"}}, Domain: "creative"})
	if err != nil {
		t.Fatal(err)
	}
	ops, err := browserops.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer ops.Close()
	bridge, err := NewBrowserMutations(svc, ops)
	if err != nil {
		t.Fatal(err)
	}
	_, err = bridge.Feedback(ctx, browserMutationTestSubject, contract.FeedbackRequest{Version: 1, IdempotencyKey: "paired-creative-rating", TaskID: out.TaskID, Action: contract.FeedbackRecord, Rating: 9, Accepted: true})
	if err != nil {
		t.Fatal(err)
	}
	next, err := svc.Run(ctx, Request{ModelID: "auto", Messages: []providers.Message{{Role: "user", Content: "Write another poem"}}, Domain: "creative"})
	if err != nil {
		t.Fatal(err)
	}
	db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	events, err := db.Read(ctx, next.TaskID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Kind == runtime.RouteSelected && event.Data.Route != nil {
			for _, candidate := range event.Data.Route.Ranked {
				if candidate.Provider == "paired-coder" && candidate.AdvisorySamples == 1 {
					return
				}
			}
		}
	}
	t.Fatal("paired model rating missing from route evidence")
}
