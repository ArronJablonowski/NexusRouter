package app

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/contextengine"
	"github.com/ArronJablonowski/NexusRouter/internal/codexbridge"
	"github.com/ArronJablonowski/NexusRouter/providers"
)

type contextCodexFixture struct{ delegateEstimatorProvider }

func (contextCodexFixture) Close() error { return nil }
func (contextCodexFixture) Models(context.Context) ([]string, error) {
	return []string{"gpt-5.6-sol"}, nil
}

func TestContextEngineCodexDoesNotRescrubFrozenMessages(t *testing.T) {
	ctx := context.Background()
	secret := ""
	svc, err := NewService(codexTaskConfig(t), func(string) string { return secret })
	if err != nil {
		t.Fatal(err)
	}
	var planned []byte
	calls := 0
	svc.codexLauncher = func(context.Context, codexbridge.LaunchSpec) (taskProvider, error) {
		return contextCodexFixture{delegateEstimatorProvider(func(_ context.Context, r providers.Request, emit func(providers.Chunk) error) error {
			calls++
			if planned != nil {
				actual, _ := json.Marshal(r.Messages)
				if string(actual) != string(planned) {
					t.Error("Codex modified frozen custom context after planning")
				}
			}
			return emit(providers.Chunk{Text: "answer", Done: true, FinishReason: "stop"})
		})}, nil
	}
	first, err := svc.Run(ctx, Request{ModelID: "brain", Prompt: "REDACTED earlier"})
	if err != nil {
		t.Fatal(err)
	}
	// Literal replacement is not idempotent when the key overlaps its marker.
	secret = "REDACTED"
	engine := applicationContextEngine{assembly: func(ctx context.Context, input contextengine.Assembly) (contextengine.Plan, error) {
		defaultContext, err := contextengine.Assemble(ctx, nil, input)
		if err != nil {
			return contextengine.Plan{}, err
		}
		planned, err = json.Marshal(defaultContext.Messages)
		if err != nil {
			return contextengine.Plan{}, err
		}
		return (contextengine.Default{}).Assemble(ctx, input)
	}}
	svc.contextEngine, svc.contextEstimator = engine, engine
	out, err := svc.Run(ctx, Request{ModelID: "brain", ContinueTaskID: first.TaskID, Prompt: "REDACTED current"})
	if err != nil || out.Text != "answer" || calls != 2 || planned == nil {
		t.Fatal(out, err, calls)
	}
}
