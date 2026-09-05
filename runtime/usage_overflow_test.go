package runtime_test

import (
	"context"
	"encoding/json"
	"math"
	"testing"

	"darwinrouter/providers"
	"darwinrouter/runtime"
)

func TestRuntimeUsageOverflowRemainsUnknown(t *testing.T) {
	db, _ := store(t)
	turn := 0
	loop := runtime.Loop{Journal: db,
		Provider: model(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
			turn++
			if turn == 1 {
				if err := emit(providers.Chunk{ToolCall: &providers.ToolCall{ID: "call", Name: "lookup", Arguments: json.RawMessage(`{}`)}}); err != nil {
					return err
				}
				return emit(providers.Chunk{Done: true, FinishReason: "tool_calls", Usage: &providers.Usage{InputTokens: math.MaxInt64, OutputTokens: 2}})
			}
			return emit(providers.Chunk{Text: "answer", Done: true, FinishReason: "stop", Usage: &providers.Usage{InputTokens: 1, OutputTokens: 3}})
		}),
		Tools: executor(func(context.Context, providers.ToolCall) (runtime.ToolResult, error) {
			return runtime.ToolResult{Content: "found", Effect: runtime.NoEffect}, nil
		}),
	}
	result, err := loop.Run(context.Background(), runRequest())
	if err != nil || result.Text != "answer" || result.Turns != 2 || result.Usage != nil {
		t.Fatal(result, err)
	}
}
