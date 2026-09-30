package runtime_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func TestToolArgumentRejectionAllowsModelCorrectionOnlyWithoutEffects(t *testing.T) {
	for _, effect := range []runtime.Effect{runtime.NoEffect, runtime.ConfirmedEffect, runtime.UncertainEffect} {
		t.Run(string(effect), func(t *testing.T) {
			db, _ := store(t)
			turns, calls := 0, 0
			loop := runtime.Loop{Journal: db, Provider: model(func(_ context.Context, req providers.Request, emit func(providers.Chunk) error) error {
				turns++
				if turns == 1 {
					return emitCall(emit)
				}
				last := req.Messages[len(req.Messages)-1]
				if last.Role != "tool" || !last.ToolFailed || !strings.Contains(last.Content, "invalid_tool_arguments") {
					t.Fatal("missing recoverable schema feedback", last)
				}
				return emit(providers.Chunk{Text: "completed without the invalid tool", Done: true, FinishReason: "stop"})
			}), Tools: executor(func(context.Context, providers.ToolCall) (runtime.ToolResult, error) {
				calls++
				return runtime.ToolResult{Effect: effect}, runtime.ErrToolArguments
			})}
			out, err := loop.Run(context.Background(), runRequest())
			if effect == runtime.NoEffect {
				if err != nil || turns != 2 || out.Text == "" {
					t.Fatal(out, err, turns)
				}
			} else if !errors.Is(err, runtime.ErrTool) || turns != 1 {
				t.Fatal("effectful rejection retried", err, turns)
			}
			if calls != 1 {
				t.Fatal("executor automatically retried a proposal", calls)
			}
		})
	}
}
