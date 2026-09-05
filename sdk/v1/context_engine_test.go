package v1_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/contextengine"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	sdk "github.com/ArronJablonowski/DarwinRouter/sdk/v1"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

type sdkFullContextEngine struct {
	contextengine.Default
	assemble func(context.Context, contextengine.Assembly) (contextengine.Plan, error)
	estimate func(context.Context, providers.Request) (int, error)
	compact  func(context.Context, sessions.Snapshot, sessions.CompactionRequest) (sessions.CompactionRequest, error)
}

func (e *sdkFullContextEngine) PrepareCompaction(ctx context.Context, source sessions.Snapshot, r sessions.CompactionRequest) (sessions.CompactionRequest, error) {
	if e.compact != nil {
		return e.compact(ctx, source, r)
	}
	return e.Default.PrepareCompaction(ctx, source, r)
}

func (e *sdkFullContextEngine) Assemble(ctx context.Context, a contextengine.Assembly) (contextengine.Plan, error) {
	if e.assemble != nil {
		return e.assemble(ctx, a)
	}
	return e.Default.Assemble(ctx, a)
}
func (e *sdkFullContextEngine) Estimate(ctx context.Context, r providers.Request) (int, error) {
	if e.estimate != nil {
		return e.estimate(ctx, r)
	}
	return e.Default.Estimate(ctx, r)
}

func TestSDKContextEngineAssemblyExecutedAndPersisted(t *testing.T) {
	for _, mode := range []string{"default", "custom_keep", "custom_omit"} {
		t.Run(mode, func(t *testing.T) {
			options, _ := sdkToolOptions(t)
			options.Overrides = map[string]string{"memory.enabled": "true", "memory.scope": "project", "memory.local_only": "true"}
			options.MemoryStore = &sdkMemoryEngine{mode: "valid"}
			options.LookupSecret = func(name string) string {
				if name == "DARWIN_API_TOKEN" {
					return "sdk-memory-secret"
				}
				return ""
			}
			var assembled, estimated atomic.Int32
			if mode == "default" {
				options.ContextEngine = contextengine.Default{}
			} else {
				options.ContextEngine = &sdkFullContextEngine{assemble: func(ctx context.Context, a contextengine.Assembly) (contextengine.Plan, error) {
					assembled.Add(1)
					deadline, ok := ctx.Deadline()
					if !ok || time.Until(deadline) > 3*time.Second {
						t.Error("unbounded assembly")
					}
					if a.Version != 1 || len(a.Memory) == 0 || len(a.Current) != 1 {
						t.Error("missing actual context tiers")
					}
					body, _ := json.Marshal(a)
					if strings.Contains(string(body), "sdk-memory-secret") {
						t.Error("assembly exposed configured secret")
					}
					// Callback mutations must not modify the materialized plan inputs.
					a.Current[0].Content = "MUTATED_CURRENT"
					for i := range a.Memory {
						a.Memory[i].Content = "MUTATED_MEMORY"
					}
					order := []contextengine.Tier{contextengine.HistoryTier, contextengine.CurrentTier}
					if mode == "custom_keep" {
						order = []contextengine.Tier{contextengine.MemoryTier, contextengine.HistoryTier, contextengine.CurrentTier}
					}
					return contextengine.Plan{Version: 1, Order: order}, nil
				}, estimate: func(_ context.Context, r providers.Request) (int, error) {
					estimated.Add(1)
					r.Messages[0].Content = "MUTATED_ESTIMATE"
					return 0, nil
				}}
			}
			var seen []providers.Message
			options.ProviderFactory = sdkProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
				return sdkProviderStream(func(_ context.Context, r providers.Request, emit func(providers.Chunk) error) error {
					seen = append([]providers.Message(nil), r.Messages...)
					return emit(providers.Chunk{Text: "answer", Done: true, FinishReason: "stop"})
				}), nil
			})
			client, err := sdk.New(options)
			if err != nil {
				t.Fatal(err)
			}
			out, err := client.Run(context.Background(), sdk.Request{Version: 1, ModelID: "chat", Prompt: "useful fact sdk-memory-secret"})
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(seen)
			if strings.Contains(string(encoded), "MUTATED_") || strings.Contains(string(encoded), "sdk-memory-secret") {
				t.Fatal("provider context leaked or mutated")
			}
			if mode == "custom_omit" && len(seen) != 1 {
				t.Fatal("optional memory omission ignored", len(seen))
			}
			if mode != "custom_omit" && len(seen) < 2 {
				t.Fatal("optional memory missing")
			}
			snapshot, err := client.InspectTask(context.Background(), out.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			if len(snapshot.Messages) != len(seen)+1 || !reflect.DeepEqual(snapshot.Messages[:len(seen)], seen) {
				t.Fatal("persisted and executed assembly differ")
			}
			if mode != "default" && (assembled.Load() != 1 || estimated.Load() < 1) {
				t.Fatal("engine not actually used", assembled.Load(), estimated.Load())
			}
		})
	}
}

func TestSDKContextEngineFailuresDenyInference(t *testing.T) {
	for _, mode := range []string{"error", "panic", "invalid_plan", "estimate_high", "estimate_error", "estimate_panic", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			options, _ := sdkToolOptions(t)
			var calls atomic.Int32
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			engine := &sdkFullContextEngine{}
			engine.assemble = func(ctx context.Context, a contextengine.Assembly) (contextengine.Plan, error) {
				switch mode {
				case "error":
					return contextengine.Plan{}, errors.New("PRIVATE_ENGINE")
				case "panic":
					panic("PRIVATE_ENGINE")
				case "invalid_plan":
					return contextengine.Plan{Version: 1, Order: []contextengine.Tier{contextengine.HistoryTier}}, nil
				case "canceled":
					cancel()
				}
				return engine.Default.Assemble(ctx, a)
			}
			engine.estimate = func(ctx context.Context, r providers.Request) (int, error) {
				switch mode {
				case "estimate_high":
					return 100000, nil
				case "estimate_error":
					return 0, errors.New("PRIVATE_ENGINE")
				case "estimate_panic":
					panic("PRIVATE_ENGINE")
				}
				return engine.Default.Estimate(ctx, r)
			}
			options.ContextEngine = engine
			options.ProviderFactory = sdkProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
				return sdkProviderStream(func(context.Context, providers.Request, func(providers.Chunk) error) error { calls.Add(1); return nil }), nil
			})
			client, err := sdk.New(options)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Run(ctx, sdk.Request{Version: 1, ModelID: "chat", Prompt: "hello"})
			if err == nil || calls.Load() != 0 || strings.Contains(err.Error(), "PRIVATE_ENGINE") {
				t.Fatal("unsafe engine failure", err, calls.Load())
			}
		})
	}
}

func TestSDKContextEngineConfigurationExclusiveAndTypedNil(t *testing.T) {
	options, _ := sdkToolOptions(t)
	var engine *sdkFullContextEngine
	options.ContextEngine = engine
	if _, err := sdk.New(options); err == nil {
		t.Fatal("typed nil engine accepted")
	}
	options.ContextEngine = contextengine.Default{}
	options.ContextEstimator = sdkContextEstimator(func(context.Context, providers.Request) (int, error) { return 0, nil })
	if _, err := sdk.New(options); err == nil {
		t.Fatal("ambiguous engine/estimator accepted")
	}
}

func TestSDKContextEngineCompactionRetainsCustomSuffix(t *testing.T) {
	options, _ := sdkToolOptions(t)
	var compacted atomic.Int32
	options.ContextEngine = &sdkFullContextEngine{compact: func(_ context.Context, source sessions.Snapshot, r sessions.CompactionRequest) (sessions.CompactionRequest, error) {
		compacted.Add(1)
		if len(source.Messages) != 4 {
			t.Error("wrong source length")
		}
		source.Messages[0].Content = "MUTATED_SOURCE"
		r.Keep = 3
		return r, nil
	}}
	var observed []providers.Message
	options.ProviderFactory = sdkProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		return sdkProviderStream(func(_ context.Context, r providers.Request, emit func(providers.Chunk) error) error {
			observed = append([]providers.Message(nil), r.Messages...)
			return emit(providers.Chunk{Text: "saved answer", Done: true, FinishReason: "stop"})
		}), nil
	})
	client, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first, err := client.Run(ctx, sdk.Request{Version: 1, ModelID: "chat", Prompt: "first question"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.Run(ctx, sdk.Request{Version: 1, ModelID: "chat", Prompt: "second question", ContinueTaskID: first.TaskID})
	if err != nil {
		t.Fatal(err)
	}
	before, err := client.InspectTask(ctx, second.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	out, err := client.Run(ctx, sdk.Request{Version: 1, ModelID: "chat", Prompt: "third question", ContinueTaskID: second.TaskID, Compaction: &sessions.CompactionRequest{Keep: 1, Summary: sessions.Summary{Decisions: []string{"Retain prior decisions"}}}})
	if err != nil || out.TaskID == "" || compacted.Load() != 1 {
		t.Fatal(out, err, compacted.Load())
	}
	// Custom keep=3 means untrusted summary pair +3 original messages +new user.
	if len(observed) != 6 || !reflect.DeepEqual(observed[2:5], before.Messages[1:]) {
		t.Fatal("custom retention not executed")
	}
	after, err := client.InspectTask(ctx, second.TaskID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("source history mutated", err)
	}
}
