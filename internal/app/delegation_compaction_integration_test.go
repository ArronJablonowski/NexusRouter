package app

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/contextengine"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

func TestPlannedParentCompactionAfterDelegationCompletion(t *testing.T) {
	for _, tc := range []struct {
		name        string
		tool        string
		arguments   json.RawMessage
		childPrompt []string
	}{
		{
			name: "delegate", tool: "delegate",
			arguments:   json.RawMessage(`{"prompt":"isolated delegate prompt","validation":"text"}`),
			childPrompt: []string{"isolated delegate prompt"},
		},
		{
			name: "delegate_batch", tool: "delegate_batch",
			arguments:   json.RawMessage(`{"tasks":[{"prompt":"isolated first prompt","validation":"text"},{"prompt":"isolated second prompt","validation":"text"}]}`),
			childPrompt: []string{"isolated first prompt", "isolated second prompt"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			svc, attempt, review := prepareDelegationCompactionPlan(t, ctx)

			var parentStreams atomic.Int32
			var childMu sync.Mutex
			childSeen := map[string]int{}
			var compactedRequest providers.Request
			call := providers.ToolCall{ID: tc.name + "-call", Name: tc.tool, Arguments: append(json.RawMessage(nil), tc.arguments...)}
			svc.providerFactory = applicationProviderFactory(func(_ context.Context, _ providers.Connection) (providers.Provider, error) {
				return delegateEstimatorProvider(func(_ context.Context, request providers.Request, emit func(providers.Chunk) error) error {
					if request.Model == "z" {
						if len(request.Messages) != 1 || request.Messages[0].Role != "user" || len(request.Tools) != 0 {
							t.Errorf("delegated child inherited parent context or tools: %+v", request)
							return errors.New("child context was not isolated")
						}
						prompt := request.Messages[0].Content
						childMu.Lock()
						childSeen[prompt]++
						childMu.Unlock()
						return emit(providers.Chunk{Text: "answer for " + prompt, Done: true, FinishReason: "stop"})
					}
					if request.Model != "a" {
						return errors.New("unexpected execution model")
					}
					switch parentStreams.Add(1) {
					case 1:
						if err := emit(providers.Chunk{ToolCall: &call}); err != nil {
							return err
						}
						return emit(providers.Chunk{Done: true, FinishReason: "tool_calls"})
					case 2:
						cloned, cloneErr := cloneProviderRequest(request)
						if cloneErr != nil {
							return cloneErr
						}
						compactedRequest = *cloned
						return emit(providers.Chunk{Text: "parent final", Done: true, FinishReason: "stop"})
					default:
						return errors.New("unexpected parent redispatch")
					}
				}), nil
			})

			read, err := telemetry.OpenReadOnly(ctx, svc.settings.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			history, err := sessions.Replay(ctx, read, attempt.TaskID)
			read.Close()
			if err != nil {
				t.Fatal(err)
			}
			prompt := "delegate before activating the reviewed parent plan"
			initial := providers.Request{
				Model: "a", Messages: append(append([]providers.Message(nil), history.Messages...), providers.Message{Role: "user", Content: prompt}),
				Tools: []providers.Tool{delegateSpec(), delegateBatchSpec()},
			}
			limit, err := providers.EstimateContext(initial)
			if err != nil {
				t.Fatal(err)
			}
			for i := range svc.settings.Models {
				svc.settings.Models[i].ContextTokens = limit
			}

			out, err := svc.Run(ctx, Request{ModelID: "a", ContinueTaskID: attempt.TaskID, Prompt: prompt})
			if err != nil || out.Text != "parent final" || parentStreams.Load() != 2 {
				t.Fatalf("result=%+v err=%v parent_streams=%d", out, err, parentStreams.Load())
			}
			childMu.Lock()
			seenCopy := make(map[string]int, len(childSeen))
			for key, value := range childSeen {
				seenCopy[key] = value
			}
			childMu.Unlock()
			for _, want := range tc.childPrompt {
				if seenCopy[want] != 1 {
					t.Fatalf("isolated child prompt executions=%v, want one execution of %q", seenCopy, want)
				}
				delete(seenCopy, want)
			}
			if len(seenCopy) != 0 {
				t.Fatal("unexpected delegated child prompts", seenCopy)
			}

			if len(compactedRequest.Messages) < 2 {
				t.Fatal("parent did not receive compacted live suffix", compactedRequest)
			}
			assistant := compactedRequest.Messages[len(compactedRequest.Messages)-2]
			toolResult := compactedRequest.Messages[len(compactedRequest.Messages)-1]
			if assistant.Role != "assistant" || len(assistant.ToolCalls) != 1 || !reflect.DeepEqual(assistant.ToolCalls[0], call) ||
				toolResult.Role != "tool" || toolResult.ToolCallID != call.ID || toolResult.Content == "" {
				t.Fatalf("activated parent plan lost exact delegate call/result: call=%+v result=%+v", assistant, toolResult)
			}
			assertDelegationResult(t, tc.tool, toolResult.Content, tc.childPrompt)

			reopened, err := telemetry.OpenReadOnly(ctx, svc.settings.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			state, err := reopened.ContextCompactionPlanForAttempt(ctx, attempt.ID)
			if err != nil || state.Status != sessions.ContextCompactionActivated || len(state.Facts) == 0 {
				t.Fatal("planned parent compaction was not activated", state, err)
			}
			activation := state.Facts[len(state.Facts)-1].Activation
			if activation == nil || activation.TaskID != out.TaskID || activation.LiveSuffixCount != 2 || len(activation.Delegations) != len(tc.childPrompt) {
				t.Fatal("activation omitted completed delegation bindings", activation)
			}
			for index, binding := range activation.Delegations {
				if binding.Validate() != nil || binding.ParentTaskID != out.TaskID || binding.Origin.ToolCallID != call.ID ||
					binding.Origin.ToolName != tc.tool || binding.EngineDigest == "" || binding.ParentPolicyDigest == "" || binding.ChildPolicyDigest == "" {
					t.Fatalf("invalid activation binding %d: %+v", index, binding)
				}
				if tc.tool == "delegate" && binding.Origin.BatchIndex != nil {
					t.Fatal("single delegation acquired batch identity", binding)
				}
				if tc.tool == "delegate_batch" && (binding.Origin.BatchIndex == nil || *binding.Origin.BatchIndex != index) {
					t.Fatalf("batch activation binding order=%d binding=%+v", index, binding)
				}
			}
			if activation.PlanDigest == "" || state.Plan == nil || activation.PlanDigest != state.Plan.PlanDigest ||
				state.Plan.Compaction.SummaryAttemptID != attempt.ID || state.Plan.Compaction.SummaryReviewID != review.ID {
				t.Fatal("activation was not bound to the reviewed plan", activation, state.Plan)
			}

			replayed, err := sessions.Replay(ctx, reopened, out.TaskID)
			if err != nil || len(replayed.Messages) < 3 {
				t.Fatal("activated parent did not replay", replayed, err)
			}
			replayCall := replayed.Messages[len(replayed.Messages)-3]
			replayResult := replayed.Messages[len(replayed.Messages)-2]
			if !reflect.DeepEqual(replayCall, assistant) || !reflect.DeepEqual(replayResult, toolResult) {
				t.Fatalf("durable replay changed exact delegate call/result: call=%+v result=%+v", replayCall, replayResult)
			}
		})
	}
}

func TestPlannedParentCompactionFailsClosedOnDelegationPolicyDrift(t *testing.T) {
	ctx := context.Background()
	svc, attempt, _ := prepareDelegationCompactionPlan(t, ctx)
	svc.settings.Workers.DelegateReadTools = true
	var constructions atomic.Int32
	svc.providerFactory = applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		constructions.Add(1)
		return nil, errors.New("provider must not be constructed")
	})
	out, err := svc.Run(ctx, Request{ModelID: "a", ContinueTaskID: attempt.TaskID, Prompt: "reject drift before dispatch"})
	if err != ErrAdmission || out.TaskID != "" || constructions.Load() != 0 {
		t.Fatalf("delegation policy drift reached execution: result=%+v err=%v constructions=%d", out, err, constructions.Load())
	}
}

func prepareDelegationCompactionPlan(t *testing.T, ctx context.Context) (*Service, sessions.SummaryAttempt, sessions.SummaryReview) {
	t.Helper()
	fixture, cfg := autoFixture(t)
	cfg.Workers.Max = 4
	cfg.Hardware.Concurrent = "4"
	cfg.Workers.DelegateModel = "z"
	cfg.Workers.DelegateMaxCalls = 4
	cfg.Runtime.AutoApprovedCompaction = true
	svc, err := NewServiceWithProviderFactory(cfg, nil, nil, nil, nil, applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		return delegateEstimatorProvider(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
			return emit(providers.Chunk{Text: "source answer", Done: true, FinishReason: "stop"})
		}), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	svc.profile = fixture.profile
	identity, err := runtime.NewContextEngineIdentity("test.delegation-context-engine", "dar-125-v1")
	if err != nil {
		t.Fatal(err)
	}
	engine := &describedApplicationContextEngine{applicationContextEngine: applicationContextEngine{Default: contextengine.Default{}}, identity: identity}
	svc.contextEngine, svc.contextEstimator = engine, engine
	source, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "delegation-compaction-source-" + strings.Repeat("history ", 300)})
	if err != nil {
		t.Fatal(err)
	}
	svc.providerFactory = applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		return delegateEstimatorProvider(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
			return emit(providers.Chunk{Text: `{"version":1,"summary":{"requirements":["Preserve the delegated parent requirement"]}}`, Done: true, FinishReason: "stop"})
		}), nil
	})
	operation, err := svc.PrepareSummary(ctx, "delegation-compaction-summary-0001", PrepareSummaryRequest{
		Version: 1, TaskID: source.TaskID, ModelID: "a", Keep: 1, MaxCost: 0,
	})
	if err != nil || operation.TerminalAttempt == nil || operation.TerminalAttempt.Draft == nil {
		t.Fatal("summary preparation failed", operation, err)
	}
	attempt := *operation.TerminalAttempt
	registry := summaryValidationRegistry(t, sessions.SummaryValidatorFunc(func(context.Context, sessions.SummaryValidationInput) (sessions.SummaryValidationDecision, error) {
		return sessions.SummaryValidationDecision{Decision: "approved", Note: "deterministic source checks passed"}, nil
	}))
	review, err := svc.ValidateSummary(ctx, attempt.ID, "", "delegation-compaction-validation-0001", "project-tests-v1", registry)
	if err != nil || review.Version != 2 || review.Decision != "approved" {
		t.Fatal("trusted validation failed", review, err)
	}
	return svc, attempt, review
}

func assertDelegationResult(t *testing.T, tool, content string, prompts []string) {
	t.Helper()
	if tool == "delegate" {
		var result struct {
			Output string `json:"untrusted_output"`
		}
		if json.Unmarshal([]byte(content), &result) != nil || result.Output != "answer for "+prompts[0] {
			t.Fatal("unexpected exact delegate result", content)
		}
		return
	}
	var batch struct {
		Results []struct {
			Output string `json:"untrusted_output"`
		} `json:"results"`
	}
	if json.Unmarshal([]byte(content), &batch) != nil || len(batch.Results) != len(prompts) {
		t.Fatal("unexpected exact delegate_batch result", content)
	}
	for index, prompt := range prompts {
		if batch.Results[index].Output != "answer for "+prompt {
			t.Fatal("delegate_batch result order changed", content)
		}
	}
}
