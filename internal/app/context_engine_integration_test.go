package app

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/contextengine"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

type applicationContextEngine struct {
	contextengine.Default
	assembly func(context.Context, contextengine.Assembly) (contextengine.Plan, error)
	compact  func(context.Context, sessions.Snapshot, sessions.CompactionRequest) (sessions.CompactionRequest, error)
	estimate func(context.Context, providers.Request) (int, error)
}

func (e applicationContextEngine) Assemble(ctx context.Context, in contextengine.Assembly) (contextengine.Plan, error) {
	if e.assembly != nil {
		return e.assembly(ctx, in)
	}
	return e.Default.Assemble(ctx, in)
}
func (e applicationContextEngine) PrepareCompaction(ctx context.Context, s sessions.Snapshot, r sessions.CompactionRequest) (sessions.CompactionRequest, error) {
	if e.compact != nil {
		return e.compact(ctx, s, r)
	}
	return e.Default.PrepareCompaction(ctx, s, r)
}
func (e applicationContextEngine) Estimate(ctx context.Context, r providers.Request) (int, error) {
	if e.estimate != nil {
		return e.estimate(ctx, r)
	}
	return e.Default.Estimate(ctx, r)
}

func TestContextEngineFreezesRoutingDispatchAndMemoryUse(t *testing.T) {
	for _, model := range []string{"a", "auto"} {
		for _, includeMemory := range []bool{false, true} {
			name := model + "/omit"
			if includeMemory {
				name = model + "/include"
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				svc, cfg := autoFixture(t)
				first, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "prior message"})
				if err != nil {
					t.Fatal(err)
				}
				db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				fact := contextMemoryFact("selected")
				fact.Content = "fact preference private-secret"
				if err := db.PutMemory(ctx, fact, 0); err != nil {
					t.Fatal(err)
				}
				svc.settings.Memory.Enabled = true
				svc.settings.Memory.Scope = "project"
				svc.secret = func(string) string { return "private-secret" }
				var assemblies, estimates, dispatches atomic.Int32
				var dispatched []providers.Message
				engine := applicationContextEngine{
					assembly: func(_ context.Context, a contextengine.Assembly) (contextengine.Plan, error) {
						if assemblies.Add(1) != 1 {
							return contextengine.Plan{}, contextengine.ErrEngine
						}
						encoded, _ := json.Marshal(a)
						if len(a.History) == 0 || len(a.Memory) == 0 || strings.Contains(string(encoded), "private-secret") {
							t.Error("callback tiers missing or credentials disclosed")
						}
						a.Current[0].Content = "CALLBACK MUTATION"
						order := []contextengine.Tier{contextengine.HistoryTier, contextengine.CurrentTier}
						if includeMemory {
							order = []contextengine.Tier{contextengine.MemoryTier, contextengine.HistoryTier, contextengine.CurrentTier}
						}
						return contextengine.Plan{Version: 1, Order: order}, nil
					},
					estimate: func(_ context.Context, r providers.Request) (int, error) {
						estimates.Add(1)
						encoded, _ := json.Marshal(r.Messages)
						if strings.Contains(string(encoded), "memory_facts") != includeMemory || strings.Contains(string(encoded), "CALLBACK MUTATION") || strings.Contains(string(encoded), "private-secret") {
							t.Error("estimator saw different/unsafe context")
						}
						return 1, nil
					},
				}
				svc.contextEngine, svc.contextEstimator = engine, engine
				svc.providerFactory = applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
					return delegateEstimatorProvider(func(_ context.Context, r providers.Request, emit func(providers.Chunk) error) error {
						dispatches.Add(1)
						encoded, _ := json.Marshal(r.Messages)
						if json.Unmarshal(encoded, &dispatched) != nil {
							t.Error("cannot snapshot dispatch")
						}
						return emit(providers.Chunk{Text: "answer", Done: true, FinishReason: "stop"})
					}), nil
				})
				out, err := svc.Run(ctx, Request{ModelID: model, ContinueTaskID: first.TaskID, Prompt: "Use fact preference private-secret"})
				if err != nil || assemblies.Load() != 1 || dispatches.Load() != 1 || estimates.Load() < 1 {
					t.Fatal(out, err, assemblies.Load(), dispatches.Load(), estimates.Load())
				}
				encoded, _ := json.Marshal(dispatched)
				if strings.Contains(string(encoded), "memory_facts") != includeMemory || strings.Contains(string(encoded), "CALLBACK MUTATION") || strings.Contains(string(encoded), "private-secret") {
					t.Fatal("dispatch diverged from selection")
				}
				if includeMemory && dispatched[0].Content != memoryInstruction {
					t.Fatal("custom tier order ignored")
				}
				events, err := db.Read(ctx, out.TaskID, 0, 1)
				if err != nil || len(events) != 1 {
					t.Fatal(err)
				}
				persisted, _ := json.Marshal(events[0].Data.Messages)
				if string(persisted) != string(encoded) {
					t.Fatal("journal and dispatch use different assemblies")
				}
				used, err := db.GetMemory(ctx, fact.Scope, fact.ID)
				if err != nil || used.LastUse.IsZero() == includeMemory {
					t.Fatal("omitted memory touched or included memory not touched", err)
				}
			})
		}
	}
}
