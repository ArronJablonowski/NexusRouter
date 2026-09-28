package runtime_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"testing"
)

func TestTrustedSubmissionClosesToolsWithoutManufacturingSuccess(t *testing.T) {
	for _, behavior := range []string{"final", "repeat", "empty", "stream_failure", "same_batch"} {
		t.Run(behavior, func(t *testing.T) {
			db, _ := store(t)
			turns, effects := 0, 0
			l := runtime.Loop{Journal: db, Provider: model(func(_ context.Context, r providers.Request, emit func(providers.Chunk) error) error {
				turns++
				if turns == 1 {
					if err := emit(providers.Chunk{ToolCall: &providers.ToolCall{ID: "save", Name: "save", Arguments: json.RawMessage(`{}`)}}); err != nil {
						return err
					}
					if behavior == "same_batch" {
						if err := emit(providers.Chunk{ToolCall: &providers.ToolCall{ID: "again", Name: "save", Arguments: json.RawMessage(`{}`)}}); err != nil {
							return err
						}
					}
					return emit(providers.Chunk{Done: true, FinishReason: "tool_calls"})
				}
				if len(r.Tools) != 0 {
					t.Fatal("submission left tools advertised")
				}
				switch behavior {
				case "repeat":
					return emitCall(emit)
				case "stream_failure":
					return &providers.Failure{Code: "invalid_stream", Partial: true}
				case "final":
					if err := emit(providers.Chunk{Text: "Submitted."}); err != nil {
						return err
					}
				}
				return emit(providers.Chunk{Done: true, FinishReason: "stop"})
			}), Tools: executor(func(context.Context, providers.ToolCall) (runtime.ToolResult, error) {
				effects++
				return runtime.ToolResult{Content: "Saved. Respond with final text; do not call tools.", Effect: runtime.ConfirmedEffect, EndToolUse: true}, nil
			})}
			r := runRequest()
			r.RequireText = true
			r.MaxTurns = 32
			r.Inference.Tools = []providers.Tool{{Name: "save", Parameters: json.RawMessage(`{"type":"object"}`)}}
			result, err := l.Run(context.Background(), r)
			if effects != 1 || turns > 2 {
				t.Fatalf("repeated effects or turns: %d/%d", effects, turns)
			}
			if behavior == "final" {
				if err != nil || result.Text != "Submitted." {
					t.Fatal(result, err)
				}
			} else if err == nil {
				t.Fatal("failed finalization became success")
			}
			if behavior == "empty" && !errors.Is(err, runtime.ErrEmptyOutput) {
				t.Fatal(err)
			}
		})
	}
}
