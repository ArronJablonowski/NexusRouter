package app

import (
	"context"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/browserops"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func TestBrowserFeedbackContextExplainsIneligibleTasks(t *testing.T) {
	for _, terminal := range []runtime.Kind{"", runtime.TaskFailed, runtime.TaskCanceled} {
		t.Run(string(terminal), func(t *testing.T) {
			svc, cfg := autoFixture(t)
			ctx := context.Background()
			db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			kinds := []runtime.Kind{runtime.TaskStarted, runtime.TurnStarted}
			if terminal != "" {
				kinds = append(kinds, terminal)
			}
			for i, kind := range kinds {
				e := runtime.Event{Version: 1, ID: string(kind), TaskID: "task", SessionID: "session", CorrelationID: "task", Sequence: int64(i + 1), Time: time.Now().UTC(), Kind: kind}
				if i == 0 {
					e.Data.Messages = []providers.Message{{Role: "user", Content: "hello"}}
				} else {
					e.TurnID, e.AttemptID = "turn", "attempt"
					e.Data.ModelID, e.Data.ProviderID = "a", "local"
				}
				if err := db.Append(ctx, int64(i), e); err != nil {
					t.Fatal(err)
				}
			}
			store, err := browserops.Open(ctx, cfg.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			mutations, err := NewBrowserMutations(svc, store)
			if err != nil {
				t.Fatal(err)
			}
			got, err := mutations.FeedbackContext(ctx, "task")
			if err != nil || got.Validate() != nil || got.FeedbackAllowed || got.DenialCode != "task_ineligible" {
				t.Fatalf("expected a usable feedback denial: %+v err=%v", got, err)
			}
		})
	}
}
