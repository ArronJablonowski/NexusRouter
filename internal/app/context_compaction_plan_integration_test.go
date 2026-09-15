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
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

type describedApplicationContextEngine struct {
	applicationContextEngine
	identity runtime.ContextEngineIdentity
}

func (e *describedApplicationContextEngine) Descriptor(context.Context) runtime.ContextEngineIdentity {
	return e.identity
}

func TestCustomContextEnginePlanActivatesAfterToolTurn(t *testing.T) {
	ctx := context.Background()
	svc, cfg := autoFixture(t)
	identity, err := runtime.NewContextEngineIdentity("test.context-engine", "dar-120-v1")
	if err != nil {
		t.Fatal(err)
	}
	engine := &describedApplicationContextEngine{
		applicationContextEngine: applicationContextEngine{Default: contextengine.Default{}},
		identity:                 identity,
	}
	svc.contextEngine, svc.contextEstimator = engine, engine

	source, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "immutable-source-" + strings.Repeat("history ", 300)})
	if err != nil {
		t.Fatal(err)
	}
	svc.providerFactory = applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		return delegateEstimatorProvider(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
			return emit(providers.Chunk{Text: `{"version":1,"summary":{"requirements":["Preserve the custom-engine source requirement"]}}`, Done: true, FinishReason: "stop"})
		}), nil
	})
	operation, err := svc.PrepareSummary(ctx, "custom-engine-summary-plan-0001", PrepareSummaryRequest{
		Version: 1, TaskID: source.TaskID, ModelID: "a", Keep: 1, MaxCost: 0,
	})
	if err != nil || operation.TerminalAttempt == nil || operation.TerminalAttempt.Draft == nil {
		t.Fatal("summary preparation failed", operation, err)
	}
	attempt := *operation.TerminalAttempt
	registry := summaryValidationRegistry(t, sessions.SummaryValidatorFunc(func(context.Context, sessions.SummaryValidationInput) (sessions.SummaryValidationDecision, error) {
		return sessions.SummaryValidationDecision{Decision: "approved", Note: "deterministic source checks passed"}, nil
	}))
	review, err := svc.ValidateSummary(ctx, attempt.ID, "", "custom-engine-validation-0001", "project-tests-v1", registry)
	if err != nil || review.Version != 2 || review.Decision != "approved" {
		t.Fatal("trusted validation failed", review, err)
	}

	var streams, toolCalls atomic.Int32
	extension := applicationExtension(t, &toolCalls)
	svc.toolExtension = extension
	svc.providerFactory = applicationProviderFactory(func(_ context.Context, connection providers.Connection) (providers.Provider, error) {
		if connection.Purpose != providers.PurposeExecution {
			t.Fatalf("unexpected provider purpose %q", connection.Purpose)
		}
		return delegateEstimatorProvider(func(_ context.Context, request providers.Request, emit func(providers.Chunk) error) error {
			switch streams.Add(1) {
			case 1:
				body, _ := json.Marshal(request.Messages)
				if !strings.Contains(string(body), "immutable-source-") || strings.Contains(string(body), "custom-engine source requirement") {
					t.Error("first turn did not retain the complete source prefix")
				}
				if err := emit(providers.Chunk{ToolCall: &providers.ToolCall{ID: "custom-plan-lookup", Name: "lookup", Arguments: json.RawMessage(`{}`)}}); err != nil {
					return err
				}
				return emit(providers.Chunk{Done: true, FinishReason: "tool_calls"})
			case 2:
				body, _ := json.Marshal(request.Messages)
				if strings.Contains(string(body), "immutable-source-") || !strings.Contains(string(body), "custom-engine source requirement") ||
					request.Messages[len(request.Messages)-2].ToolCalls[0].ID != "custom-plan-lookup" ||
					request.Messages[len(request.Messages)-1].ToolCallID != "custom-plan-lookup" {
					t.Error("activated plan lost its summary or live tool suffix")
				}
				return emit(providers.Chunk{Text: "custom plan answer", Done: true, FinishReason: "stop"})
			default:
				t.Error("unexpected provider redispatch")
				return nil
			}
		}), nil
	})

	read, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	history, err := sessions.Replay(ctx, read, source.TaskID)
	read.Close()
	if err != nil {
		t.Fatal(err)
	}
	prompt := "use the lookup"
	initial := providers.Request{Model: "a", Messages: append(append([]providers.Message(nil), history.Messages...), providers.Message{Role: "user", Content: prompt}), Tools: extension.Catalog()}
	limit, err := providers.EstimateContext(initial)
	if err != nil {
		t.Fatal(err)
	}
	svc.settings.Runtime.AutoApprovedCompaction = true
	for i := range svc.settings.Models {
		svc.settings.Models[i].ContextTokens = limit
	}
	out, err := svc.Run(ctx, Request{ModelID: "a", ContinueTaskID: source.TaskID, Prompt: prompt})
	if err != nil || out.Text != "custom plan answer" || streams.Load() != 2 || toolCalls.Load() != 1 {
		t.Fatalf("result=%+v err=%v streams=%d tools=%d", out, err, streams.Load(), toolCalls.Load())
	}

	reopened, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	state, err := reopened.ContextCompactionPlanForAttempt(ctx, attempt.ID)
	if err != nil || state.Status != sessions.ContextCompactionActivated || state.Plan == nil || len(state.Facts) == 0 {
		t.Fatal("activated lifecycle did not survive reopen", state, err)
	}
	activation := state.Facts[len(state.Facts)-1].Activation
	if activation == nil || activation.TaskID != out.TaskID || activation.LiveSuffixCount != 2 {
		t.Fatal("activation did not bind the live tool suffix", activation)
	}
	replayed, err := sessions.Replay(ctx, reopened, out.TaskID)
	if err != nil || replayed.Compaction == nil || replayed.Compaction.SummaryAttemptID != attempt.ID || replayed.Compaction.SummaryReviewID != review.ID {
		t.Fatal("activated continuation did not replay", replayed, err)
	}
	body, _ := json.Marshal(replayed.Messages)
	if strings.Contains(string(body), "immutable-source-") || !strings.Contains(string(body), "trusted evidence") || !strings.Contains(string(body), "custom plan answer") {
		t.Fatal("replay lost compacted context or live suffix", string(body))
	}
}

func TestCustomContextEnginePlanRejectsDescriptorDrift(t *testing.T) {
	ctx := context.Background()
	svc, cfg := autoFixture(t)
	identity, err := runtime.NewContextEngineIdentity("test.context-engine", "dar-120-v1")
	if err != nil {
		t.Fatal(err)
	}
	engine := &describedApplicationContextEngine{
		applicationContextEngine: applicationContextEngine{Default: contextengine.Default{}},
		identity:                 identity,
	}
	svc.contextEngine, svc.contextEstimator = engine, engine
	source, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "descriptor-source-" + strings.Repeat("history ", 300)})
	if err != nil {
		t.Fatal(err)
	}
	svc.providerFactory = applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		return delegateEstimatorProvider(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
			return emit(providers.Chunk{Text: `{"version":1,"summary":{"requirements":["Preserve descriptor source"]}}`, Done: true, FinishReason: "stop"})
		}), nil
	})
	operation, err := svc.PrepareSummary(ctx, "custom-engine-descriptor-drift-0001", PrepareSummaryRequest{
		Version: 1, TaskID: source.TaskID, ModelID: "a", Keep: 1, MaxCost: 0,
	})
	if err != nil || operation.TerminalAttempt == nil {
		t.Fatal(operation, err)
	}
	attempt := *operation.TerminalAttempt
	registry := summaryValidationRegistry(t, sessions.SummaryValidatorFunc(func(context.Context, sessions.SummaryValidationInput) (sessions.SummaryValidationDecision, error) {
		return sessions.SummaryValidationDecision{Decision: "approved", Note: "deterministic source checks passed"}, nil
	}))
	if review, validateErr := svc.ValidateSummary(ctx, attempt.ID, "", "custom-engine-drift-validation-0001", "project-tests-v1", registry); validateErr != nil || review.Version != 2 || review.Decision != "approved" {
		t.Fatal(review, validateErr)
	}
	engine.identity, err = runtime.NewContextEngineIdentity("test.context-engine", "dar-120-v2")
	if err != nil {
		t.Fatal(err)
	}
	var constructions atomic.Int32
	svc.providerFactory = applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		constructions.Add(1)
		return nil, context.Canceled
	})
	svc.settings.Runtime.AutoApprovedCompaction = true
	out, runErr := svc.Run(ctx, Request{ModelID: "a", ContinueTaskID: source.TaskID, Prompt: "continue after drift"})
	if out.TaskID != "" || runErr != ErrAdmission || constructions.Load() != 0 {
		t.Fatalf("drift reached execution: result=%+v err=%v constructions=%d", out, runErr, constructions.Load())
	}
	reopened, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	state, err := reopened.ContextCompactionPlanForAttempt(ctx, attempt.ID)
	if err != nil || state.Status != sessions.ContextCompactionStarted || state.Plan != nil {
		t.Fatal("descriptor drift advanced lifecycle", state, err)
	}
}
