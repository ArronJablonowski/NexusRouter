package runtime_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func TestToolResultTypedFailurePreservesEffect(t *testing.T) {
	for _, effect := range []runtime.Effect{runtime.NoEffect, runtime.ConfirmedEffect} {
		for _, failed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s_failed_%t", effect, failed), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				db, _ := store(t)
				turns, executions := 0, 0
				loop := runtime.Loop{Journal: db, Provider: model(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
					turns++
					if turns == 1 {
						return emitCall(emit)
					}
					return emit(providers.Chunk{Text: "final answer", Done: true, FinishReason: "stop"})
				}), Tools: executor(func(_ context.Context, _ providers.ToolCall) (runtime.ToolResult, error) {
					executions++
					return runtime.ToolResult{Content: "typed operation outcome", Effect: effect, Failed: failed}, nil
				})}
				result, err := loop.Run(ctx, runRequest())
				if executions != 1 {
					t.Fatalf("tool executed %d times", executions)
				}
				if failed {
					if !errors.Is(err, runtime.ErrTool) || turns != 1 || result.Retryable {
						t.Fatalf("failed result continued or permitted retry: turns=%d err=%v", turns, err)
					}
				} else if err != nil || turns != 2 || result.Text != "final answer" {
					t.Fatalf("successful result changed: turns=%d err=%v", turns, err)
				}
				events, err := db.Read(ctx, "task", 0, 100)
				if err != nil || len(events) == 0 {
					t.Fatal("missing durable trajectory", err)
				}
				completed := 0
				for _, event := range events {
					if event.Kind != runtime.ToolCompleted {
						continue
					}
					completed++
					code := ""
					if failed {
						code = "tool_failed"
					}
					if event.Data.Code != code || event.Data.Effect != effect || event.Data.Text != "typed operation outcome" {
						t.Fatal("durable failure lost independent effect or content")
					}
				}
				terminal := runtime.TaskCompleted
				if failed {
					terminal = runtime.TaskFailed
				}
				if completed != 1 || events[len(events)-1].Kind != terminal {
					t.Fatal("incorrect terminal trajectory")
				}
			})
		}
	}
}
