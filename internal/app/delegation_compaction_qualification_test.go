package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/contextengine"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

// TestDAR126DelegationCompactionAdversarialQualification exercises the complete
// planned-parent boundary. Component tests cover the individual worker failure
// classifications; this test proves none of them can accidentally authorize a
// compaction activation or parent redispatch.
func TestDAR126DelegationCompactionAdversarialQualification(t *testing.T) {
	tests := []struct {
		name       string
		child      func(context.Context, func(providers.Chunk) error) error
		beforeRun  func(*testing.T, *Service)
		afterChild func(*testing.T, context.Context, *Service, sessions.SummaryAttempt, sessions.SummaryReview)
		streams    int32
		status     sessions.ContextCompactionLifecycleKind
		childCalls int32
		toolOK     bool
		reason     string
		childCode  string
	}{
		{
			name: "review_revoked_after_child_output", child: successfulQualificationChild, streams: 1, childCalls: 1, toolOK: true,
			afterChild: func(t *testing.T, ctx context.Context, svc *Service, attempt sessions.SummaryAttempt, review sessions.SummaryReview) {
				if _, err := svc.ReviewSummary(ctx, attempt.ID, review.ID, "rejected", "qualification revocation"); err != nil {
					t.Error(err)
				}
			},
		},
		{
			name: "policy_drift", child: successfulQualificationChild, status: sessions.ContextCompactionStarted,
			beforeRun: func(_ *testing.T, svc *Service) {
				svc.settings.Workers.DelegateReadTools = true
			},
		},
		{
			name: "engine_drift", child: successfulQualificationChild, status: sessions.ContextCompactionStarted,
			beforeRun: func(t *testing.T, svc *Service) {
				identity, err := runtime.NewContextEngineIdentity("test.delegation-context-engine", "dar-126-drift")
				if err != nil {
					t.Error(err)
					return
				}
				engine := &describedApplicationContextEngine{applicationContextEngine: applicationContextEngine{Default: contextengine.Default{}}, identity: identity}
				svc.contextEngine, svc.contextEstimator = engine, engine
			},
		},
		{
			name: "child_canceled", streams: 1, childCalls: 1, reason: "execution_failed", childCode: "execution_failed",
			child: func(context.Context, func(providers.Chunk) error) error { return context.Canceled },
		},
		{
			name: "child_timeout", streams: 1, childCalls: 1, reason: "execution_failed", childCode: "execution_failed",
			child: func(context.Context, func(providers.Chunk) error) error { return context.DeadlineExceeded },
		},
		{
			name: "child_panic_uncertain", streams: 1, childCalls: 1, reason: "execution_failed", childCode: "execution_failed",
			child: func(context.Context, func(providers.Chunk) error) error { panic("private qualification panic") },
		},
		{
			name: "child_oversized_output", streams: 1, childCalls: 1, reason: "budget_exhausted", childCode: "budget_exhausted",
			child: func(_ context.Context, emit func(providers.Chunk) error) error {
				return emit(providers.Chunk{Text: strings.Repeat("x", (128<<10)+1), Done: true, FinishReason: "stop"})
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			svc, attempt, review := prepareDelegationCompactionPlan(t, ctx)
			if test.beforeRun != nil {
				test.beforeRun(t, svc)
			}
			var parentStreams atomic.Int32
			var childCalls atomic.Int32
			mutated := atomic.Bool{}
			call := providers.ToolCall{ID: "qualification-call", Name: "delegate", Arguments: []byte(`{"prompt":"qualification child","validation":"text"}`)}
			svc.providerFactory = applicationProviderFactory(func(_ context.Context, _ providers.Connection) (providers.Provider, error) {
				return delegateEstimatorProvider(func(ctx context.Context, request providers.Request, emit func(providers.Chunk) error) error {
					if request.Model == "z" {
						childCalls.Add(1)
						err := test.child(ctx, emit)
						if err == nil && test.afterChild != nil && mutated.CompareAndSwap(false, true) {
							test.afterChild(t, ctx, svc, attempt, review)
						}
						return err
					}
					if parentStreams.Add(1) != 1 {
						return errors.New("parent redispatched after rejected activation")
					}
					if err := emit(providers.Chunk{ToolCall: &call}); err != nil {
						return err
					}
					return emit(providers.Chunk{Done: true, FinishReason: "tool_calls"})
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
			prompt := "qualification parent"
			initial := providers.Request{Model: "a", Messages: append(append([]providers.Message(nil), history.Messages...), providers.Message{Role: "user", Content: prompt}), Tools: []providers.Tool{delegateSpec(), delegateBatchSpec()}}
			limit, err := providers.EstimateContext(initial)
			if err != nil {
				t.Fatal(err)
			}
			for i := range svc.settings.Models {
				svc.settings.Models[i].ContextTokens = limit
			}

			request := Request{ModelID: "a", ContinueTaskID: attempt.TaskID, Prompt: prompt}
			out, runErr := svc.Run(ctx, request)
			wantErr := error(runtime.ErrPersistence)
			if test.streams == 0 {
				wantErr = ErrAdmission
			}
			if !errors.Is(runErr, wantErr) || out.Text != "" || parentStreams.Load() != test.streams || childCalls.Load() != test.childCalls {
				t.Fatalf("unsafe qualification outcome: result=%+v err=%v parent_streams=%d child_calls=%d", out, runErr, parentStreams.Load(), childCalls.Load())
			}
			if test.afterChild != nil && !mutated.Load() {
				t.Fatal("post-delegation adversarial mutation did not run")
			}

			store, err := telemetry.OpenReadOnly(ctx, svc.settings.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			state, err := store.ContextCompactionPlanForAttempt(ctx, attempt.ID)
			if err != nil {
				t.Fatal(err)
			}
			want := test.status
			if want == "" {
				want = sessions.ContextCompactionApproved
			}
			if state.Status != want {
				t.Fatalf("plan state=%s want=%s", state.Status, want)
			}
			for _, fact := range state.Facts {
				if fact.Kind == sessions.ContextCompactionActivated || fact.Activation != nil {
					t.Fatal("rejected qualification activated compaction", fact)
				}
			}
			if test.streams == 0 {
				if out.TaskID != "" {
					t.Fatal("pre-admission drift created a task", out.TaskID)
				}
				return
			}
			if out.TaskID == "" {
				t.Fatal("post-dispatch failure omitted parent task identity")
			}
			events, err := store.Read(ctx, out.TaskID, 0, 100)
			if err != nil || len(events) == 0 {
				t.Fatalf("missing durable parent evidence: events=%+v err=%v", events, err)
			}
			var completion *runtime.Event
			for i := range events {
				if events[i].Kind == runtime.ContextCompacted {
					t.Fatal("rejected qualification persisted ContextCompacted", events[i])
				}
				if events[i].Kind == runtime.TaskCompleted || events[i].Kind == runtime.TaskFailed || events[i].Kind == runtime.TaskCanceled {
					t.Fatal("activation rejection incorrectly terminalized parent task", events[i])
				}
				if events[i].Kind == runtime.ToolCompleted && events[i].Data.ToolCallID == call.ID {
					if completion != nil {
						t.Fatal("duplicate durable delegate completion")
					}
					completion = &events[i]
				}
			}
			if completion == nil || completion.Data.Effect != runtime.NoEffect {
				t.Fatal("missing effect-free durable delegate completion", completion)
			}
			if test.toolOK {
				var result struct {
					WorkID      string `json:"work_task_id"`
					ExecutionID string `json:"execution_task_id"`
					Output      string `json:"untrusted_output"`
				}
				if completion.Data.Code != "" || json.Unmarshal([]byte(completion.Data.Text), &result) != nil || result.WorkID == "" || result.ExecutionID == "" || result.Output != "qualified child answer" {
					t.Fatal("successful child evidence was not durable", completion)
				}
			} else {
				var report delegateFailure
				if completion.Data.Code != "tool_failed" || json.Unmarshal([]byte(completion.Data.Text), &report) != nil || report.Version != 1 || report.Error != "delegate_unavailable_or_rejected" || report.Reason != test.reason || report.WorkID == "" || report.ExecutionID == "" || len(report.Evidence) != 2 {
					t.Fatal("child failure was not durably classified", completion)
				}
				wantCodes := map[string]string{report.WorkID: "worker_failed", report.ExecutionID: test.childCode}
				for _, ref := range report.Evidence {
					if wantCodes[ref.TaskID] == "" || ref.Code != wantCodes[ref.TaskID] || ref.Kind != runtime.TaskFailed || ref.Sequence < 1 {
						t.Fatal("unexpected durable child failure reference", ref)
					}
					page, readErr := store.ReadEventPage(ctx, ref.TaskID, ref.Sequence-1, 1)
					if readErr != nil || page.HasMore || page.HeadSequence != ref.Sequence || len(page.Events) != 1 || page.Events[0].Kind != ref.Kind || page.Events[0].Data.Code != ref.Code {
						t.Fatalf("child failure reference does not match durable journal: ref=%+v page=%+v err=%v", ref, page, readErr)
					}
					delete(wantCodes, ref.TaskID)
				}
				if len(wantCodes) != 0 {
					t.Fatal("child failure omitted durable work or execution evidence", wantCodes)
				}
			}
		})
	}
}

func successfulQualificationChild(_ context.Context, emit func(providers.Chunk) error) error {
	return emit(providers.Chunk{Text: "qualified child answer", Done: true, FinishReason: "stop"})
}
