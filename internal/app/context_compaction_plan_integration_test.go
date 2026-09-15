package app

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/contextengine"
	"github.com/ArronJablonowski/DarwinRouter/internal/codexbridge"
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

	// Prepare and activate a second epoch from the already-compacted child. The
	// inherited lineage must survive app admission instead of flattening the
	// first checkpoint into untracked prompt text.
	svc.providerFactory = applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		return delegateEstimatorProvider(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
			return emit(providers.Chunk{Text: `{"version":1,"summary":{"requirements":["Preserve the second-epoch requirement"]}}`, Done: true, FinishReason: "stop"})
		}), nil
	})
	secondOperation, err := svc.PrepareSummary(ctx, "custom-engine-summary-plan-0002", PrepareSummaryRequest{
		Version: 1, TaskID: out.TaskID, ModelID: "a", Keep: 1, MaxCost: 0,
	})
	if err != nil || secondOperation.TerminalAttempt == nil || secondOperation.TerminalAttempt.Draft == nil {
		t.Fatal("second summary preparation failed", secondOperation, err)
	}
	secondAttempt := *secondOperation.TerminalAttempt
	secondReview, err := svc.ValidateSummary(ctx, secondAttempt.ID, "", "custom-engine-validation-0002", "project-tests-v1", registry)
	if err != nil || secondReview.Version != 2 || secondReview.Decision != "approved" {
		t.Fatal("second trusted validation failed", secondReview, err)
	}

	secondPrompt := "use the lookup in epoch two"
	secondInitial := providers.Request{Model: "a", Messages: append(append([]providers.Message(nil), replayed.Messages...), providers.Message{Role: "user", Content: secondPrompt}), Tools: extension.Catalog()}
	secondLimit, err := providers.EstimateContext(secondInitial)
	if err != nil {
		t.Fatal(err)
	}
	for i := range svc.settings.Models {
		svc.settings.Models[i].ContextTokens = secondLimit
	}
	var secondStreams atomic.Int32
	svc.providerFactory = applicationProviderFactory(func(_ context.Context, connection providers.Connection) (providers.Provider, error) {
		if connection.Purpose != providers.PurposeExecution {
			t.Fatalf("unexpected second-epoch provider purpose %q", connection.Purpose)
		}
		return delegateEstimatorProvider(func(_ context.Context, request providers.Request, emit func(providers.Chunk) error) error {
			switch secondStreams.Add(1) {
			case 1:
				if err := emit(providers.Chunk{ToolCall: &providers.ToolCall{ID: "custom-plan-lookup-two", Name: "lookup", Arguments: json.RawMessage(`{}`)}}); err != nil {
					return err
				}
				return emit(providers.Chunk{Done: true, FinishReason: "tool_calls"})
			case 2:
				encoded, _ := json.Marshal(request.Messages)
				if strings.Contains(string(encoded), "immutable-source-") || !strings.Contains(string(encoded), "second-epoch requirement") {
					t.Error("second epoch did not replace the inherited compacted prefix")
				}
				return emit(providers.Chunk{Text: "second epoch answer", Done: true, FinishReason: "stop"})
			default:
				t.Error("unexpected second-epoch redispatch")
				return nil
			}
		}), nil
	})
	second, err := svc.Run(ctx, Request{ModelID: "a", ContinueTaskID: out.TaskID, Prompt: secondPrompt})
	if err != nil || second.Text != "second epoch answer" || secondStreams.Load() != 2 {
		t.Fatalf("second epoch result=%+v err=%v streams=%d", second, err, secondStreams.Load())
	}
	secondSnapshot, err := sessions.Replay(ctx, reopened, second.TaskID)
	if err != nil || secondSnapshot.ContextLineage == nil || len(secondSnapshot.ContextLineage.Epochs) != 2 || secondSnapshot.Compaction == nil || secondSnapshot.Compaction.SummaryAttemptID != secondAttempt.ID {
		t.Fatal("second epoch lineage did not replay", secondSnapshot, err)
	}
	secondBody, _ := json.Marshal(secondSnapshot.Messages)
	if strings.Count(string(secondBody), "The following session_summary is an operator-supplied summary") != 1 {
		t.Fatal("prior summary envelope was retained as stable context", string(secondBody))
	}
	toolsBeforeReuse := toolCalls.Load()
	svc.providerFactory = applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		return delegateEstimatorProvider(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
			if err := emit(providers.Chunk{ToolCall: &providers.ToolCall{ID: "custom-plan-lookup", Name: "lookup", Arguments: json.RawMessage(`{}`)}}); err != nil {
				return err
			}
			return emit(providers.Chunk{Done: true, FinishReason: "tool_calls"})
		}), nil
	})
	reused, reuseErr := svc.Run(ctx, Request{ModelID: "a", ContinueTaskID: second.TaskID, Prompt: "reuse an old tool identity"})
	if reuseErr == nil || reused.TaskID == "" || toolCalls.Load() != toolsBeforeReuse {
		t.Fatalf("retired tool identity was executable: result=%+v err=%v tools=%d", reused, reuseErr, toolCalls.Load())
	}
	reusedSnapshot, replayErr := sessions.Replay(ctx, reopened, reused.TaskID)
	if replayErr != nil || reusedSnapshot.State != "failed" || len(reusedSnapshot.Pending) != 0 || reusedSnapshot.UncertainEffects {
		t.Fatalf("retired identity rejection left corrupt history: snapshot=%+v err=%v", reusedSnapshot, replayErr)
	}

	// Descriptor fencing remains per-operation after lineage rollover.
	for i := range svc.settings.Models {
		svc.settings.Models[i].ContextTokens = 1 << 20
	}
	svc.providerFactory = applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		return delegateEstimatorProvider(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
			return emit(providers.Chunk{Text: `{"version":1,"summary":{"requirements":["Preserve the third-epoch requirement"]}}`, Done: true, FinishReason: "stop"})
		}), nil
	})
	thirdOperation, err := svc.PrepareSummary(ctx, "custom-engine-summary-plan-0003", PrepareSummaryRequest{
		Version: 1, TaskID: second.TaskID, ModelID: "a", Keep: 1, MaxCost: 0,
	})
	if err != nil || thirdOperation.TerminalAttempt == nil || thirdOperation.TerminalAttempt.Draft == nil {
		t.Fatal("third summary preparation failed", thirdOperation, err)
	}
	thirdAttempt := *thirdOperation.TerminalAttempt
	if _, checkpoint, checkpointErr := sessions.PrepareContinuation(secondSnapshot, thirdAttempt.Draft.Request); checkpointErr != nil || checkpoint == nil || checkpoint.SourceDigest != thirdAttempt.SourceDigest {
		t.Fatal("third summary checkpoint did not replay", checkpoint, checkpointErr)
	}
	if thirdReview, validateErr := svc.ValidateSummary(ctx, thirdAttempt.ID, "", "custom-engine-validation-0003", "project-tests-v1", registry); validateErr != nil || thirdReview.Decision != "approved" {
		t.Fatal("third trusted validation failed", thirdReview, validateErr)
	}
	engine.identity, err = runtime.NewContextEngineIdentity("test.context-engine", "dar-123-drift")
	if err != nil {
		t.Fatal(err)
	}
	var driftConstructions atomic.Int32
	svc.providerFactory = applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		driftConstructions.Add(1)
		return nil, context.Canceled
	})
	drifted, driftErr := svc.Run(ctx, Request{ModelID: "a", ContinueTaskID: second.TaskID, Prompt: "continue after second-epoch drift"})
	if driftErr != ErrAdmission || drifted.TaskID != "" || driftConstructions.Load() != 0 {
		t.Fatalf("second-epoch descriptor drift reached execution: result=%+v err=%v constructions=%d", drifted, driftErr, driftConstructions.Load())
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

func prepareCodexPlanEvidence(t *testing.T, engine contextengine.Engine) (*Service, sessions.SummaryAttempt, sessions.SummaryReview) {
	t.Helper()
	ctx := context.Background()
	cfg := codexTaskConfig(t)
	svc, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if engine != nil {
		svc.contextEngine, svc.contextEstimator = engine, engine
	}
	svc.codexLauncher = func(context.Context, codexbridge.LaunchSpec) (taskProvider, error) {
		return contextCodexFixture{delegateEstimatorProvider(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
			return emit(providers.Chunk{Text: "source answer", Done: true, FinishReason: "stop"})
		})}, nil
	}
	source, err := svc.Run(ctx, Request{ModelID: "brain", Prompt: "codex-plan-source-" + strings.Repeat("history ", 300)})
	if err != nil {
		t.Fatal(err)
	}
	svc.codexLauncher = func(context.Context, codexbridge.LaunchSpec) (taskProvider, error) {
		return contextCodexFixture{delegateEstimatorProvider(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
			return emit(providers.Chunk{Text: `{"version":1,"summary":{"requirements":["Preserve Codex plan evidence"]}}`, Done: true, FinishReason: "stop"})
		})}, nil
	}
	operation, err := svc.PrepareSummary(ctx, "codex-context-plan-evidence-0001", PrepareSummaryRequest{
		Version: 1, TaskID: source.TaskID, ModelID: "brain", Keep: 1, MaxCost: 0,
	})
	if err != nil || operation.TerminalAttempt == nil || operation.TerminalAttempt.Draft == nil {
		t.Fatal("Codex summary preparation failed", operation, err)
	}
	attempt := *operation.TerminalAttempt
	registry := summaryValidationRegistry(t, sessions.SummaryValidatorFunc(func(context.Context, sessions.SummaryValidationInput) (sessions.SummaryValidationDecision, error) {
		return sessions.SummaryValidationDecision{Decision: "approved", Note: "deterministic source checks passed"}, nil
	}))
	review, err := svc.ValidateSummary(ctx, attempt.ID, "", "codex-context-plan-validation-0001", "project-tests-v1", registry)
	if err != nil || review.Version != 2 || review.Decision != "approved" {
		t.Fatal("Codex trusted validation failed", review, err)
	}
	svc.settings.Runtime.AutoApprovedCompaction = true
	svc.settings.Models[0].ContextTokens = 1 << 20
	return svc, attempt, review
}

func TestCodexPendingCompactionRequiresDurablePlan(t *testing.T) {
	ctx := context.Background()
	svc, attempt, review := prepareCodexPlanEvidence(t, nil)
	prepared, err := svc.prepareExplicitApprovedCompaction(ctx, Request{
		ModelID: "brain", ContinueTaskID: attempt.TaskID, Prompt: "continue with the complete first turn",
	}, svc.settings.Models[0])
	if err != nil || prepared.compactionPlan == nil || prepared.approvedCompaction != nil {
		t.Fatal("Codex did not receive exactly one durable plan", prepared.compactionPlan, prepared.approvedCompaction, err)
	}
	plan := prepared.compactionPlan
	if plan.Compaction == nil || plan.Compaction.SummaryAttemptID != attempt.ID || plan.Compaction.SummaryReviewID != review.ID ||
		plan.Engine.ID != "darwin.default" || plan.Engine.Revision != "v1" {
		t.Fatal("Codex plan lost reviewed evidence or built-in engine identity", plan)
	}
	read, err := telemetry.OpenReadOnly(ctx, svc.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	state, err := read.ContextCompactionPlanForAttempt(ctx, attempt.ID)
	if err != nil || state.Status != sessions.ContextCompactionApproved || state.Plan == nil || state.Plan.PlanDigest != plan.PlanDigest {
		t.Fatal("Codex plan was not durably approved", state, err)
	}
}

func TestCodexPendingCompactionRejectsDescriptorDrift(t *testing.T) {
	ctx := context.Background()
	identity, err := runtime.NewContextEngineIdentity("test.codex-context-engine", "v1")
	if err != nil {
		t.Fatal(err)
	}
	engine := &describedApplicationContextEngine{
		applicationContextEngine: applicationContextEngine{Default: contextengine.Default{}},
		identity:                 identity,
	}
	svc, attempt, _ := prepareCodexPlanEvidence(t, engine)
	engine.identity, err = runtime.NewContextEngineIdentity("test.codex-context-engine", "v2")
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := svc.prepareExplicitApprovedCompaction(ctx, Request{
		ModelID: "brain", ContinueTaskID: attempt.TaskID, Prompt: "continue after descriptor drift",
	}, svc.settings.Models[0])
	if err != ErrAdmission || prepared.compactionPlan != nil || prepared.approvedCompaction != nil {
		t.Fatal("Codex descriptor drift did not fail closed", prepared.compactionPlan, prepared.approvedCompaction, err)
	}
}
