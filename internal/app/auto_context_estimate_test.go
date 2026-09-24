package app

import (
	"context"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func TestAutomaticContextTierUsesSelectedModelEstimate(t *testing.T) {
	ctx := context.Background()
	svc, cfg := autoFixture(t)
	for i := range svc.settings.Models {
		svc.settings.Models[i].ContextTokens = 131072
	}
	svc.contextEstimator = auxiliaryContextEstimator(func(_ context.Context, request providers.Request) (int, error) {
		if request.Model == "a" {
			return 40000, nil
		}
		return 80000, nil
	})
	result, err := svc.Run(ctx, Request{Prompt: "hello", Domain: "code"})
	if err != nil || result.Text != "a" {
		t.Fatalf("unexpected route: %+v %v", result, err)
	}
	db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	events, err := db.Read(ctx, result.TaskID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Kind == runtime.TaskStarted {
			if event.Data.ContextTokens != 65536 {
				t.Fatalf("allocated %d tokens for a model-specific 40000-token estimate", event.Data.ContextTokens)
			}
			return
		}
	}
	t.Fatal("missing task start")
}
