package app

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/contextengine"
	"github.com/ArronJablonowski/NexusRouter/providers"
)

func TestContextEngineDelegationKeepsChildScoped(t *testing.T) {
	fixture, cfg := autoFixture(t)
	cfg.Workers.Max, cfg.Hardware.Concurrent = 2, "2"
	cfg.Workers.DelegateModel, cfg.Workers.DelegateMaxCalls = "z", 1
	var parentCalls, childCalls, parentPlans, childPlans atomic.Int32
	engine := applicationContextEngine{assembly: func(ctx context.Context, a contextengine.Assembly) (contextengine.Plan, error) {
		if len(a.Current) != 1 || len(a.History)+len(a.Memory)+len(a.Skills) != 0 {
			t.Error("unexpected ambient context")
		}
		if a.Current[0].Content == "child request" {
			childPlans.Add(1)
		} else if a.Current[0].Content == "parent request" {
			parentPlans.Add(1)
		} else {
			t.Error("unknown context")
		}
		return (contextengine.Default{}).Assemble(ctx, a)
	}}
	factory := applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		return delegateEstimatorProvider(func(_ context.Context, r providers.Request, emit func(providers.Chunk) error) error {
			if r.Model == "z" {
				childCalls.Add(1)
				if len(r.Tools) != 0 || len(r.Messages) != 1 || r.Messages[0].Content != "child request" {
					t.Error("child acquired parent context or permissions")
				}
				return emit(providers.Chunk{Text: "child answer", Done: true, FinishReason: "stop"})
			}
			if parentCalls.Add(1) == 1 {
				if err := emit(providers.Chunk{ToolCall: &providers.ToolCall{ID: "delegate", Name: "delegate", Arguments: json.RawMessage(`{"prompt":"child request","validation":"text"}`)}}); err != nil {
					return err
				}
				return emit(providers.Chunk{Done: true, FinishReason: "tool_calls"})
			}
			return emit(providers.Chunk{Text: "parent answer", Done: true, FinishReason: "stop"})
		}), nil
	})
	svc, err := NewServiceWithContextEngine(cfg, nil, nil, nil, nil, factory, nil, nil, nil, engine)
	if err != nil {
		t.Fatal(err)
	}
	svc.profile = fixture.profile
	out, err := svc.Run(context.Background(), Request{ModelID: "a", Prompt: "parent request"})
	if err != nil || out.Text != "parent answer" || childCalls.Load() != 1 || parentCalls.Load() != 2 || childPlans.Load() != 1 || parentPlans.Load() != 1 {
		t.Fatal(out, err, parentCalls.Load(), childCalls.Load(), parentPlans.Load(), childPlans.Load())
	}
}
