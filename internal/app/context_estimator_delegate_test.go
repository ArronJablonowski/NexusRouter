package app

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
)

type delegateContextEstimator func(context.Context, providers.Request) (int, error)

func (f delegateContextEstimator) Estimate(ctx context.Context, r providers.Request) (int, error) {
	return f(ctx, r)
}

type delegateEstimatorProvider func(context.Context, providers.Request, func(providers.Chunk) error) error

func (p delegateEstimatorProvider) Stream(ctx context.Context, r providers.Request, e func(providers.Chunk) error) error {
	return p(ctx, r, e)
}
func (delegateEstimatorProvider) Models(context.Context) ([]string, error) {
	return []string{"a", "z"}, nil
}

func TestDelegateInheritsContextEstimatorWithoutExpandingPermissions(t *testing.T) {
	for _, mode := range []string{"allow", "deny"} {
		t.Run(mode, func(t *testing.T) {
			fixture, cfg := autoFixture(t)
			cfg.Workers.Max = 2
			cfg.Hardware.Concurrent = "2"
			cfg.Workers.DelegateModel = "z"
			cfg.Workers.DelegateMaxCalls = 1
			var parentCalls, childCalls, parentEstimates, childEstimates atomic.Int32
			var toolOutput string
			assertChild := func(r providers.Request) {
				if len(r.Tools) != 0 || len(r.Messages) != 1 || r.Messages[0].Role != "user" || r.Messages[0].Content != "isolated child prompt" {
					t.Error("child acquired ambient context or tools", r)
				}
			}
			factory := applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
				return delegateEstimatorProvider(func(_ context.Context, r providers.Request, emit func(providers.Chunk) error) error {
					if r.Model == "z" {
						childCalls.Add(1)
						assertChild(r)
						return emit(providers.Chunk{Text: "child answer", Done: true, FinishReason: "stop"})
					}
					if r.Model != "a" {
						t.Error("unexpected model", r.Model)
					}
					if parentCalls.Add(1) == 1 {
						if err := emit(providers.Chunk{ToolCall: &providers.ToolCall{ID: "delegate-call", Name: "delegate", Arguments: json.RawMessage(`{"prompt":"isolated child prompt","validation":"text"}`)}}); err != nil {
							return err
						}
						return emit(providers.Chunk{Done: true, FinishReason: "tool_calls"})
					}
					for _, m := range r.Messages {
						if m.Role == "tool" {
							toolOutput = m.Content
						}
					}
					return emit(providers.Chunk{Text: "parent final", Done: true, FinishReason: "stop"})
				}), nil
			})
			svc, err := NewServiceWithProviderFactory(cfg, nil, nil, nil, nil, factory)
			if err != nil {
				t.Fatal(err)
			}
			svc.profile = fixture.profile
			svc.contextEstimator = delegateContextEstimator(func(_ context.Context, r providers.Request) (int, error) {
				if r.Model == "z" {
					childEstimates.Add(1)
					assertChild(r)
					if mode == "deny" {
						return 8193, nil
					}
				} else {
					parentEstimates.Add(1)
					if r.Model != "a" {
						t.Error("unexpected estimated model", r.Model)
					}
				}
				return 1, nil
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "ambient parent secret"})
			if err != nil || result.Text != "parent final" || parentCalls.Load() != 2 || parentEstimates.Load() < 2 || childEstimates.Load() < 1 {
				t.Fatal(result, err, parentCalls.Load(), parentEstimates.Load(), childEstimates.Load())
			}
			if mode == "deny" {
				if childCalls.Load() != 0 || !validDelegateRejection(toolOutput) {
					t.Fatal("denied child dispatched or leaked output", childCalls.Load(), toolOutput)
				}
			} else if childCalls.Load() != 1 || !strings.Contains(toolOutput, `"untrusted_output":"child answer"`) {
				t.Fatal(childCalls.Load(), toolOutput)
			}
			db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			snapshot, err := db.TaskSnapshot(ctx, result.TaskID)
			if err != nil || snapshot.State != "completed" {
				t.Fatal(snapshot, err)
			}
		})
	}
}
