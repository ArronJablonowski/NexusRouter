package runtime_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func TestLoopPersistsAndExecutesCanonicalToolArguments(t *testing.T) {
	db, _ := store(t)
	turns := 0
	want := `{"a":true,"z":{"a":1,"n":9007199254740993}}`
	loop := runtime.Loop{
		Journal: db,
		Provider: model(func(_ context.Context, request providers.Request, emit func(providers.Chunk) error) error {
			turns++
			if turns == 1 {
				call := providers.ToolCall{ID: "call", Name: "lookup", Arguments: json.RawMessage(" { \"z\" : {\"n\":9007199254740993,\"a\":1}, \"a\" : true } ")}
				if err := emit(providers.Chunk{ToolCall: &call}); err != nil {
					return err
				}
				return emit(providers.Chunk{Done: true, FinishReason: "tool_calls"})
			}
			if got := string(request.Messages[1].ToolCalls[0].Arguments); got != want {
				t.Fatalf("continuation arguments = %q", got)
			}
			return emit(providers.Chunk{Text: "done", Done: true, FinishReason: "stop"})
		}),
		Tools: executor(func(ctx context.Context, call providers.ToolCall) (runtime.ToolResult, error) {
			if got := string(call.Arguments); got != want {
				t.Fatalf("execution arguments = %q", got)
			}
			events, err := db.Read(ctx, "task", 0, 100)
			if err != nil || len(events) < 3 || string(events[2].Data.ToolCalls[0].Arguments) != want {
				t.Fatalf("durable arguments mismatch: %+v, %v", events, err)
			}
			return runtime.ToolResult{Content: "ok", Effect: runtime.NoEffect}, nil
		}),
	}
	result, err := loop.Run(context.Background(), runRequest())
	if err != nil || result.Text != "done" || turns != 2 {
		t.Fatalf("result=%+v turns=%d err=%v", result, turns, err)
	}
}

func TestLoopChargesCanonicalToolArgumentBytes(t *testing.T) {
	db, _ := store(t)
	raw := json.RawMessage(`{"x":"<"}`)
	call := providers.ToolCall{ID: "call", Name: "lookup", Arguments: raw}
	loop := runtime.Loop{
		Journal: db,
		Provider: model(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
			return emit(providers.Chunk{ToolCall: &call})
		}),
		Tools: executor(func(context.Context, providers.ToolCall) (runtime.ToolResult, error) {
			t.Fatal("canonical output larger than the budget was executed")
			return runtime.ToolResult{}, nil
		}),
	}
	request := runRequest()
	request.MaxOutputBytes = len(raw) + len(call.ID) + len(call.Name)
	if _, err := loop.Run(context.Background(), request); !errors.Is(err, runtime.ErrLimit) {
		t.Fatalf("error = %v, want output limit", err)
	}
}
