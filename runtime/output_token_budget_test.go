package runtime_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func TestOutputTokenBudgetShrinksAcrossToolTurns(t *testing.T) {
	journal, _ := store(t)
	turn := 0
	loop := runtime.Loop{Journal: journal, Provider: model(func(_ context.Context, request providers.Request, emit func(providers.Chunk) error) error {
		turn++
		want := int64(5)
		if turn == 2 {
			want = 3
		}
		if request.MaxOutputTokens != want {
			t.Fatalf("turn %d output ceiling=%d want=%d", turn, request.MaxOutputTokens, want)
		}
		if turn == 1 {
			if err := emit(providers.Chunk{ToolCall: &providers.ToolCall{ID: "call", Name: "lookup", Arguments: []byte(`{}`)}}); err != nil {
				return err
			}
			if err := emit(providers.Chunk{Usage: &providers.Usage{InputTokens: 3, OutputTokens: 2}}); err != nil {
				return err
			}
			return emit(providers.Chunk{Done: true, FinishReason: "tool_calls"})
		}
		if err := emit(providers.Chunk{Text: "done"}); err != nil {
			return err
		}
		if err := emit(providers.Chunk{Usage: &providers.Usage{InputTokens: 4, OutputTokens: 3}}); err != nil {
			return err
		}
		return emit(providers.Chunk{Done: true, FinishReason: "stop"})
	}), Tools: executor(func(context.Context, providers.ToolCall) (runtime.ToolResult, error) {
		return runtime.ToolResult{Content: "found", Effect: runtime.NoEffect}, nil
	})}
	request := runRequest()
	request.Inference.MaxOutputTokens = 5
	result, err := loop.Run(context.Background(), request)
	if err != nil || result.Text != "done" || result.Turns != 2 || result.Usage == nil || result.Usage.OutputTokens != 5 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestOutputTokenBudgetRejectsUnknownUsageBeforeToolEffect(t *testing.T) {
	journal, _ := store(t)
	executed := false
	loop := runtime.Loop{Journal: journal, Provider: model(func(_ context.Context, request providers.Request, emit func(providers.Chunk) error) error {
		if request.MaxOutputTokens != 5 {
			t.Fatalf("output ceiling=%d", request.MaxOutputTokens)
		}
		return emitCall(emit)
	}), Tools: executor(func(context.Context, providers.ToolCall) (runtime.ToolResult, error) {
		executed = true
		return runtime.ToolResult{Content: "unsafe", Effect: runtime.ConfirmedEffect}, nil
	})}
	request := runRequest()
	request.Inference.MaxOutputTokens = 5
	result, err := loop.Run(context.Background(), request)
	if !errors.Is(err, runtime.ErrLimit) || result.Turns != 1 || executed {
		t.Fatalf("result=%+v err=%v executed=%v", result, err, executed)
	}
}

func TestOutputTokenBudgetRejectsInvalidBoundsBeforeDispatch(t *testing.T) {
	for _, limit := range []int64{-1, providers.MaxOutputTokens + 1} {
		called := false
		loop := runtime.Loop{Journal: journal(func(context.Context, int64, runtime.Event) error {
			t.Fatal("invalid request was persisted")
			return nil
		}), Provider: model(func(context.Context, providers.Request, func(providers.Chunk) error) error {
			called = true
			return nil
		})}
		request := runRequest()
		request.Inference.MaxOutputTokens = limit
		if _, err := loop.Run(context.Background(), request); !errors.Is(err, runtime.ErrInvalidRun) || called {
			t.Fatalf("limit=%d err=%v called=%v", limit, err, called)
		}
	}
}

func TestOutputTokenBudgetRejectsDuplicateUsageBeforeToolEffect(t *testing.T) {
	journal := &compactionJournal{}
	executed, calls := false, 0
	loop := runtime.Loop{Journal: journal, Provider: model(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
		calls++
		if calls > 1 {
			return emit(providers.Chunk{Text: "unexpected second turn", Usage: &providers.Usage{OutputTokens: 1}, Done: true, FinishReason: "stop"})
		}
		if err := emit(providers.Chunk{ToolCall: &providers.ToolCall{ID: "call", Name: "lookup", Arguments: []byte(`{}`)}}); err != nil {
			return err
		}
		if err := emit(providers.Chunk{Usage: &providers.Usage{OutputTokens: 100}}); err != nil {
			return err
		}
		// Even if an adapter ignores the callback error, the runtime must not
		// execute the proposed tool using this smaller replacement measurement.
		_ = emit(providers.Chunk{Usage: &providers.Usage{OutputTokens: 1}})
		_ = emit(providers.Chunk{Done: true, FinishReason: "tool_calls"})
		return nil
	}), Tools: executor(func(context.Context, providers.ToolCall) (runtime.ToolResult, error) {
		executed = true
		return runtime.ToolResult{Content: "unsafe", Effect: runtime.ConfirmedEffect}, nil
	})}
	request := runRequest()
	request.Inference.MaxOutputTokens = 5
	result, err := loop.Run(context.Background(), request)
	if !errors.Is(err, runtime.ErrProtocol) || executed || calls != 1 || result.Retryable || result.Usage != nil {
		t.Fatal(result, err, executed, calls)
	}
	for _, event := range journal.events {
		if event.Kind == runtime.ToolStarted || event.Kind == runtime.TurnCompleted {
			t.Fatal("ambiguous usage committed", event)
		}
	}
}
