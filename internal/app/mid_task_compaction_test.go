package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/contextengine"
	"github.com/ArronJablonowski/DarwinRouter/internal/codexbridge"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/resources"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func TestApprovedMidTaskCompactionPreservesLiveToolSuffix(t *testing.T) {
	for _, selection := range []string{"a", "auto"} {
		t.Run(selection, func(t *testing.T) {
			ctx := context.Background()
			svc, cfg := autoFixture(t)
			old := "immutable-source-" + strings.Repeat("history ", 300)
			source, err := svc.Run(ctx, Request{ModelID: "a", Prompt: old})
			if err != nil {
				t.Fatal(err)
			}
			attempt, review := approvedOverflowSummary(t, svc, cfg, source.TaskID)
			var constructions, streams, tools atomic.Int32
			extension := applicationExtension(t, &tools)
			svc.toolExtension = extension
			svc.providerFactory = applicationProviderFactory(func(_ context.Context, connection providers.Connection) (providers.Provider, error) {
				if connection.Purpose == providers.PurposeExecution {
					constructions.Add(1)
				}
				return delegateEstimatorProvider(func(_ context.Context, request providers.Request, emit func(providers.Chunk) error) error {
					switch streams.Add(1) {
					case 1:
						body, _ := json.Marshal(request.Messages)
						if !strings.Contains(string(body), "immutable-source-") || strings.Contains(string(body), "provider-overflow source requirement") {
							t.Error("first turn did not use full source history")
						}
						if err := emit(providers.Chunk{ToolCall: &providers.ToolCall{ID: "lookup-call", Name: "lookup", Arguments: json.RawMessage(`{}`)}}); err != nil {
							return err
						}
						return emit(providers.Chunk{Done: true, FinishReason: "tool_calls"})
					case 2:
						body, _ := json.Marshal(request.Messages)
						if strings.Contains(string(body), "immutable-source-") || !strings.Contains(string(body), "provider-overflow source requirement") ||
							request.Messages[len(request.Messages)-2].ToolCalls[0].ID != "lookup-call" || request.Messages[len(request.Messages)-1].ToolCallID != "lookup-call" {
							t.Error("compacted request lost summary or live tool pair")
						}
						return emit(providers.Chunk{Text: "compacted answer", Done: true, FinishReason: "stop"})
					default:
						t.Error("unexpected provider redispatch")
						return nil
					}
				}), nil
			})
			db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			history, err := sessions.Replay(ctx, db, source.TaskID)
			db.Close()
			if err != nil {
				t.Fatal(err)
			}
			prompt := "use the lookup"
			full := providers.Request{Model: "a", Messages: append(append([]providers.Message(nil), history.Messages...), providers.Message{Role: "user", Content: prompt}), Tools: extension.Catalog()}
			limit, err := providers.EstimateContext(full)
			if err != nil {
				t.Fatal(err)
			}
			svc.settings.Runtime.AutoApprovedCompaction = true
			for i := range svc.settings.Models {
				svc.settings.Models[i].ContextTokens = limit
			}
			out, err := svc.Run(ctx, Request{ModelID: selection, ContinueTaskID: source.TaskID, Prompt: prompt})
			if err != nil || out.Text != "compacted answer" || constructions.Load() != 1 || streams.Load() != 2 || tools.Load() != 1 {
				t.Fatal(out, err, constructions.Load(), streams.Load(), tools.Load())
			}
			db, err = telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			events, err := db.Read(ctx, out.TaskID, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			compactIndex, toolIndex, nextTurn := -1, -1, -1
			for i, event := range events {
				switch event.Kind {
				case runtime.ToolCompleted:
					toolIndex = i
				case runtime.ContextCompacted:
					compactIndex = i
					if event.Data.Compaction == nil || event.Data.Compaction.SummaryAttemptID != attempt.ID || event.Data.Compaction.SummaryReviewID != review.ID {
						t.Fatal("missing approved compaction provenance", event)
					}
				case runtime.TurnStarted:
					if compactIndex >= 0 && nextTurn < 0 {
						nextTurn = i
					}
				}
			}
			if toolIndex < 0 || compactIndex != toolIndex+1 || nextTurn != compactIndex+1 {
				t.Fatal("unsafe compaction ordering", toolIndex, compactIndex, nextTurn, events)
			}
			replayed, err := sessions.Replay(ctx, db, out.TaskID)
			if err != nil || replayed.Compaction == nil || replayed.Compaction.SummaryAttemptID != attempt.ID {
				t.Fatal("mid-task compaction did not replay", replayed, err)
			}
			encoded, _ := json.Marshal(replayed.Messages)
			if strings.Contains(string(encoded), "immutable-source-") || !strings.Contains(string(encoded), "trusted evidence") {
				t.Fatal("replay lost compacted/live context", string(encoded))
			}
		})
	}
}

func TestMidTaskCompactionActivationBudgetTerminalizesWithRealStore(t *testing.T) {
	ctx := context.Background()
	svc, cfg := autoFixture(t)
	source, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "source " + strings.Repeat("history ", 100)})
	if err != nil {
		t.Fatal(err)
	}
	approvedOverflowSummary(t, svc, cfg, source.TaskID)
	var streams, tools atomic.Int32
	svc.toolExtension = applicationExtension(t, &tools)
	svc.providerFactory = applicationProviderFactory(func(_ context.Context, _ providers.Connection) (providers.Provider, error) {
		return delegateEstimatorProvider(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
			streams.Add(1)
			if err := emit(providers.Chunk{ToolCall: &providers.ToolCall{ID: "budget-call", Name: "lookup", Arguments: json.RawMessage(`{}`)}}); err != nil {
				return err
			}
			return emit(providers.Chunk{Done: true, FinishReason: "tool_calls"})
		}), nil
	})
	const contextLimit = 2 << 20
	svc.settings.Runtime.AutoApprovedCompaction = true
	svc.settings.Models[0].ContextTokens = contextLimit
	// This journal-exhaustion fixture intentionally uses a large allocation;
	// otherwise the normal 32K working tier correctly rejects its initial input.
	svc.settings.Models[0].DefaultContextTokens = contextLimit
	svc.contextEstimator = auxiliaryContextEstimator(func(_ context.Context, request providers.Request) (int, error) {
		hasTool, hasSource := false, false
		for _, message := range request.Messages {
			hasTool = hasTool || message.Role == "tool"
			hasSource = hasSource || strings.Contains(message.Content, "source history")
		}
		if hasTool && hasSource {
			return contextLimit + 1, nil
		}
		return contextLimit, nil
	})
	prompt := strings.Repeat("p", (1<<20)-128)
	out, runErr := svc.Run(ctx, Request{ModelID: "a", ContinueTaskID: source.TaskID, Prompt: prompt})
	if out.TaskID == "" || !errors.Is(runErr, runtime.ErrJournalLimit) || streams.Load() != 1 || tools.Load() != 1 {
		t.Fatal("activation budget did not fail terminally before redispatch", out, runErr, streams.Load(), tools.Load())
	}
	db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	snapshot, err := db.TaskSnapshot(ctx, out.TaskID)
	if err != nil || snapshot.State != "failed" || snapshot.Compaction != nil {
		t.Fatal("activation-budget task is not durably failed", snapshot.State, snapshot.Compaction, err)
	}
	events, err := db.Read(ctx, out.TaskID, 0, 100)
	if err != nil || events[len(events)-1].Kind != runtime.TaskFailed || events[len(events)-1].Data.Code != "journal_exhausted" {
		t.Fatal("reserved activation terminal is missing", events, err)
	}
	for _, event := range events {
		if event.Kind == runtime.ContextCompacted {
			t.Fatal("rejected activation was committed", event)
		}
	}
}

func TestPendingCompactionSkipsUnsupportedAssemblyAndRedaction(t *testing.T) {
	for _, mode := range []string{"custom-engine", "redacted-source"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			svc, cfg := autoFixture(t)
			source, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "source-secret " + strings.Repeat("history ", 100)})
			if err != nil {
				t.Fatal(err)
			}
			approvedOverflowSummary(t, svc, cfg, source.TaskID)
			svc.settings.Runtime.AutoApprovedCompaction = true
			model := svc.settings.Models[0]
			model.ContextTokens = 1 << 20
			svc.settings.Models[0] = model
			switch mode {
			case "custom-engine":
				svc.contextEngine = contextengine.Default{}
			case "redacted-source":
				svc.secret = func(name string) string {
					if name == "DARWIN_API_TOKEN" {
						return "source-secret"
					}
					return ""
				}
			}
			prepared, err := svc.prepareExplicitApprovedCompaction(ctx, Request{ModelID: "a", ContinueTaskID: source.TaskID, Prompt: "continue"}, model)
			if err != nil || prepared.approvedCompaction != nil || prepared.SummaryAttemptID != "" || prepared.Compaction != nil {
				t.Fatal("unsupported pending compaction was prepared", prepared, err)
			}
		})
	}
}

func TestCodexPendingCompactionRejectsLegacyApprovalWithoutPlan(t *testing.T) {
	ctx := context.Background()
	svc, cfg := autoFixture(t)
	source, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "source " + strings.Repeat("history ", 100)})
	if err != nil {
		t.Fatal(err)
	}
	approvedOverflowSummary(t, svc, cfg, source.TaskID)
	svc.settings.Runtime.AutoApprovedCompaction = true
	svc.settings.Models[0].ContextTokens = 1 << 20
	svc.settings.Providers[0].Kind = "codex_app_server"

	prepared, err := svc.prepareExplicitApprovedCompaction(ctx, Request{ModelID: "a", ContinueTaskID: source.TaskID, Prompt: "continue"}, svc.settings.Models[0])
	if err != nil || prepared.approvedCompaction != nil || prepared.compactionPlan != nil || prepared.SummaryAttemptID != "" || prepared.Compaction != nil {
		t.Fatal("legacy approval authorized Codex pending compaction", prepared, err)
	}
}

func TestAutoCompactionPreparationFailurePrecedesManagedResidencyMutation(t *testing.T) {
	ctx := context.Background()
	svc, fixture := managedResidencyFixture(t)
	source, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "source " + strings.Repeat("history ", 100)})
	if err != nil {
		t.Fatal(err)
	}
	attempt, _ := approvedOverflowSummary(t, svc, svc.settings, source.TaskID)
	raw, err := sql.Open("sqlite", svc.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec(`UPDATE summary_attempts SET body='{}' WHERE id=?`, attempt.ID); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}
	svc.settings.Runtime.AutoApprovedCompaction = true
	fixture.mu.Lock()
	fixture.resident = "z"
	fixture.unloads, fixture.streams = 0, 0
	fixture.mu.Unlock()

	out, runErr := svc.Run(ctx, Request{ModelID: "auto", ContinueTaskID: source.TaskID, Prompt: "continue"})
	if out.TaskID != "" || !errors.Is(runErr, ErrAdmission) {
		t.Fatal("malformed approved summary was not rejected before admission", out, runErr)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.resident != "z" || fixture.unloads != 0 || fixture.streams != 0 {
		t.Fatal("summary preparation failure mutated managed residency", fixture.resident, fixture.unloads, fixture.streams)
	}
}

type compactionDiscoveryProvider struct{}

func (compactionDiscoveryProvider) Models(context.Context) ([]string, error) {
	return []string{"a", "gpt-5.6-sol", "worker"}, nil
}

func (compactionDiscoveryProvider) Stream(context.Context, providers.Request, func(providers.Chunk) error) error {
	return errors.New("discovery provider cannot execute")
}

type compactionToolTaskProvider struct {
	calls *atomic.Int32
}

type compactionFinalTaskProvider struct{}

func (*compactionFinalTaskProvider) Close() error { return nil }
func (*compactionFinalTaskProvider) Models(context.Context) ([]string, error) {
	return []string{"gpt-5.6-sol"}, nil
}
func (*compactionFinalTaskProvider) Stream(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
	return emit(providers.Chunk{Text: "source answer", Done: true, FinishReason: "stop"})
}

func (*compactionToolTaskProvider) Close() error { return nil }
func (*compactionToolTaskProvider) Models(context.Context) ([]string, error) {
	return []string{"gpt-5.6-sol"}, nil
}
func (p *compactionToolTaskProvider) Stream(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
	if p.calls.Add(1) == 1 {
		if err := emit(providers.Chunk{ToolCall: &providers.ToolCall{ID: "rerank-delegate", Name: "delegate", Arguments: json.RawMessage(`{"prompt":"inspect","validation":"go_source"}`)}}); err != nil {
			return err
		}
		return emit(providers.Chunk{Done: true, FinishReason: "tool_calls"})
	}
	return emit(providers.Chunk{Text: "unexpected compacted answer", Done: true, FinishReason: "stop"})
}

func TestAutoCapacityRerankDiscardsRejectedCandidateCompaction(t *testing.T) {
	ctx := context.Background()
	cfg := codexTaskConfig(t)
	svc, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc.profile = func(context.Context) (resources.Snapshot, error) {
		return resources.Snapshot{Time: time.Now(), CPUs: 4, TotalRAM: 1000, AvailableRAM: 1000}, nil
	}
	svc.codexLauncher = func(context.Context, codexbridge.LaunchSpec) (taskProvider, error) {
		return &compactionFinalTaskProvider{}, nil
	}
	source, err := svc.Run(ctx, Request{ModelID: "brain", Prompt: "source " + strings.Repeat("history ", 300)})
	if err != nil {
		t.Fatal(err)
	}
	_, review := approvedOverflowSummary(t, svc, cfg, source.TaskID)

	zero := 0.0
	svc.settings.Runtime.AutoApprovedCompaction = true
	svc.settings.Providers[0].Kind = "openai_compatible"
	svc.settings.Providers[0].Endpoint = "https://example.invalid"
	svc.settings.Providers[0].Executable = ""
	svc.settings.Providers = append(svc.settings.Providers, config.Provider{ID: "local", Kind: "ollama", Endpoint: "http://127.0.0.1:11434"})
	svc.settings.Models = append(svc.settings.Models, config.Model{ID: "a", Provider: "local", Model: "a", Locality: "local", RAMBytes: 2000, ContextTokens: 1 << 20, EstimatedCost: &zero, Capabilities: []string{"chat"}})
	svc.settings.Models = append(svc.settings.Models, config.Model{ID: "worker", Provider: "local", Model: "worker", Locality: "local", RAMBytes: 1, ContextTokens: 1 << 20, EstimatedCost: &zero, Capabilities: []string{"chat"}})
	svc.settings.Workers.Max = 2
	svc.settings.Workers.DelegateModel = "worker"
	svc.settings.Workers.DelegateMaxCalls = 1
	svc.settings.Routing.Exploration = 0
	var streams atomic.Int32
	svc.providerFactory = applicationProviderFactory(func(_ context.Context, connection providers.Connection) (providers.Provider, error) {
		if connection.Purpose == providers.PurposeDiscovery {
			return compactionDiscoveryProvider{}, nil
		}
		if connection.ID == "codex" && connection.Purpose == providers.PurposeExecution {
			return &compactionToolTaskProvider{calls: &streams}, nil
		}
		if connection.ID == "local" && connection.Purpose == providers.PurposeExecution {
			return delegateEstimatorProvider(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
				return emit(providers.Chunk{Text: "package evidence\n", Done: true, FinishReason: "stop"})
			}), nil
		}
		return nil, errors.New("unexpected provider construction")
	})
	request := Request{ModelID: "auto", ContinueTaskID: source.TaskID, Prompt: "delegate the inspection"}
	db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	local := svc.settings.Models[len(svc.settings.Models)-2]
	prepared, inference, err := svc.prepareExplicitInference(ctx, db, request, local, nil)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	limit, err := providers.EstimateContext(inference)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	limit += 100
	for i := range svc.settings.Models {
		svc.settings.Models[i].ContextTokens = limit
	}
	local.ContextTokens = limit
	probe, err := svc.prepareExplicitApprovedCompaction(ctx, prepared, local)
	if err != nil || probe.approvedCompaction == nil {
		t.Fatal("first candidate did not establish the rejected-plan premise", err)
	}
	svc.contextEstimator = auxiliaryContextEstimator(func(_ context.Context, request providers.Request) (int, error) {
		hasTool, hasSource := false, false
		for _, message := range request.Messages {
			hasTool = hasTool || message.Role == "tool"
			hasSource = hasSource || strings.Contains(message.Content, "source history")
		}
		if hasTool && hasSource {
			return limit + 1, nil
		}
		return limit, nil
	})
	var profiles atomic.Int32
	svc.profile = func(context.Context) (resources.Snapshot, error) {
		if profiles.Add(1) == 2 {
			if _, err := svc.ReviewSummary(ctx, review.AttemptID, review.ID, "rejected", "Revoked while the rejected local candidate was being admitted"); err != nil {
				t.Error(err)
			}
		}
		return resources.Snapshot{Time: time.Now(), CPUs: 4, TotalRAM: 1000, AvailableRAM: 1000}, nil
	}

	out, runErr := svc.Run(ctx, request)
	if out.TaskID == "" || !errors.Is(runErr, runtime.ErrContextOverflow) || streams.Load() != 1 {
		t.Fatal("capacity rerank retained or misused the rejected candidate plan", out, runErr, streams.Load())
	}
	read, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	events, err := read.Read(ctx, out.TaskID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Kind == runtime.ContextCompacted {
			t.Fatal("rejected local candidate's approved plan reached the Codex rerank", event)
		}
	}
}
