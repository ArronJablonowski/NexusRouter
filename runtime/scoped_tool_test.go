package runtime_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

type scopedExecutor struct {
	legacy executor
	scoped func(context.Context, runtime.ToolExecution) (runtime.ToolResult, error)
}

func (s scopedExecutor) Execute(ctx context.Context, call providers.ToolCall) (runtime.ToolResult, error) {
	return s.legacy(ctx, call)
}

func (s scopedExecutor) ExecuteScoped(ctx context.Context, execution runtime.ToolExecution) (runtime.ToolResult, error) {
	return s.scoped(ctx, execution)
}

func TestScopedToolExecutionIdentityAndDurableBoundary(t *testing.T) {
	for _, boundary := range []string{"success", "persistence_failure", "cancel_after_commit"} {
		t.Run(boundary, func(t *testing.T) {
			s, _ := store(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			legacyCalls, scopedCalls, turns := 0, 0, 0
			l := runtime.Loop{Provider: model(func(_ context.Context, r providers.Request, emit func(providers.Chunk) error) error {
				turns++
				if turns == 1 {
					return emitCall(emit)
				}
				if string(r.Messages[1].ToolCalls[0].Arguments) != `{}` {
					t.Fatal("scoped executor mutated conversation arguments")
				}
				return emit(providers.Chunk{Text: "answer", Done: true, FinishReason: "stop"})
			}), Journal: journal(func(ctx context.Context, sequence int64, event runtime.Event) error {
				if boundary == "persistence_failure" && event.Kind == runtime.ToolStarted {
					return errors.New("fixture commit failure")
				}
				err := s.Append(ctx, sequence, event)
				if err == nil && boundary == "cancel_after_commit" && event.Kind == runtime.ToolStarted {
					cancel()
				}
				return err
			}), Tools: scopedExecutor{
				legacy: func(context.Context, providers.ToolCall) (runtime.ToolResult, error) {
					legacyCalls++
					return runtime.ToolResult{}, errors.New("legacy must not execute")
				},
				scoped: func(ctx context.Context, execution runtime.ToolExecution) (runtime.ToolResult, error) {
					scopedCalls++
					events, err := s.Read(ctx, "task", 0, 100)
					if err != nil || len(events) == 0 {
						t.Fatalf("read durable intent: %v", err)
					}
					last := events[len(events)-1]
					if last.Kind != runtime.ToolStarted || execution.TaskID != last.TaskID || execution.SessionID != last.SessionID || execution.TurnID != last.TurnID || execution.AttemptID != last.AttemptID || execution.TurnID == "" || execution.AttemptID == "" {
						t.Fatalf("execution identity does not match durable intent: %+v / %+v", execution, last)
					}
					if execution.Call.ID != last.Data.ToolCallID || execution.Call.Name != last.Data.ToolName || string(execution.Call.Arguments) != `{}` {
						t.Fatalf("incorrect scoped call: %+v", execution.Call)
					}
					execution.Call.Arguments[0] = '['
					return runtime.ToolResult{Content: "found", Effect: runtime.NoEffect}, nil
				},
			}}
			result, err := l.Run(ctx, runRequest())
			if legacyCalls != 0 {
				t.Fatal("legacy executor was used despite scoped capability")
			}
			switch boundary {
			case "success":
				if err != nil || scopedCalls != 1 || result.Text != "answer" {
					t.Fatalf("result=%+v err=%v scoped calls=%d", result, err, scopedCalls)
				}
			case "persistence_failure":
				if !errors.Is(err, runtime.ErrPersistence) || scopedCalls != 0 {
					t.Fatalf("dispatch crossed failed persistence: err=%v calls=%d", err, scopedCalls)
				}
			case "cancel_after_commit":
				if !errors.Is(err, context.Canceled) || scopedCalls != 0 {
					t.Fatalf("dispatch crossed cancellation: err=%v calls=%d", err, scopedCalls)
				}
			}
		})
	}
}
