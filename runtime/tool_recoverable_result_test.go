package runtime_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func TestRecoverableToolFailureAllowsFreshRepairCall(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, _ := store(t)
	turns := 0
	counts := map[string]int{}
	loop := runtime.Loop{Journal: db, Provider: model(func(_ context.Context, request providers.Request, emit func(providers.Chunk) error) error {
		turns++
		if turns > 1 {
			last := request.Messages[len(request.Messages)-1]
			wantID, wantText := "failed-call", "choose another path"
			if turns == 3 {
				wantID, wantText = "repair-call", "created"
			}
			if last.Role != "tool" || last.ToolCallID != wantID || last.Content != wantText || last.ToolFailed != (turns == 2) {
				t.Fatal("tool failure pairing or metadata lost")
			}
			events, err := db.Read(ctx, "task", 0, 100)
			if err != nil || len(events) < 2 || events[len(events)-2].Kind != runtime.ToolCompleted {
				t.Fatal("repair turn preceded durable tool result", err)
			}
		}
		if turns < 3 {
			id := "failed-call"
			if turns == 2 {
				id = "repair-call"
			}
			if err := emit(providers.Chunk{ToolCall: &providers.ToolCall{ID: id, Name: "lookup", Arguments: json.RawMessage(`{}`)}}); err != nil {
				return err
			}
			return emit(providers.Chunk{Done: true, FinishReason: "tool_calls"})
		}
		return emit(providers.Chunk{Text: "repaired", Done: true, FinishReason: "stop"})
	}), Tools: executor(func(_ context.Context, call providers.ToolCall) (runtime.ToolResult, error) {
		counts[call.ID]++
		if call.ID == "failed-call" {
			return runtime.ToolResult{Content: "choose another path", Effect: runtime.NoEffect, Failed: true, Recoverable: true}, nil
		}
		return runtime.ToolResult{Content: "created", Effect: runtime.ConfirmedEffect}, nil
	})}
	result, err := loop.Run(ctx, runRequest())
	if err != nil || result.Text != "repaired" || turns != 3 || counts["failed-call"] != 1 || counts["repair-call"] != 1 {
		t.Fatal("repair replayed a call or failed", err, counts)
	}
	events, err := db.Read(ctx, "task", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Kind == runtime.ToolCompleted && event.Data.ToolCallID == "failed-call" && (event.Data.Code != "tool_failed" || event.Data.Effect != runtime.NoEffect) {
			t.Fatal("recoverability erased failure evidence")
		}
	}
}

func TestRecoverableToolFailureBoundaries(t *testing.T) {
	for _, mode := range []string{"max_turns", "cancel", "persist", "generic_error", "confirmed", "uncertain", "not_failed"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			db, _ := store(t)
			want := runtime.ToolResult{Content: "bounded failure", Effect: runtime.NoEffect, Failed: true, Recoverable: true}
			var handlerErr error
			switch mode {
			case "generic_error":
				handlerErr = errors.New("handler error")
			case "confirmed":
				want.Effect = runtime.ConfirmedEffect
			case "uncertain":
				want.Effect = runtime.UncertainEffect
			case "not_failed":
				want.Failed = false
			}
			turns, executions := 0, 0
			loop := runtime.Loop{Journal: journal(func(c context.Context, expected int64, event runtime.Event) error {
				if event.Kind == runtime.ToolCompleted && mode == "persist" {
					return errors.New("fixture persistence failure")
				}
				err := db.Append(c, expected, event)
				if event.Kind == runtime.ToolCompleted && mode == "cancel" {
					cancel()
				}
				return err
			}), Provider: model(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
				turns++
				if mode == "max_turns" && turns == 2 {
					if err := emit(providers.Chunk{ToolCall: &providers.ToolCall{ID: "budget-call", Name: "lookup", Arguments: json.RawMessage(`{}`)}}); err != nil {
						return err
					}
					return emit(providers.Chunk{Done: true, FinishReason: "tool_calls"})
				}
				return emitCall(emit)
			}), Tools: executor(func(context.Context, providers.ToolCall) (runtime.ToolResult, error) {
				executions++
				return want, handlerErr
			})}
			request := runRequest()
			if mode == "max_turns" {
				request.MaxTurns = 2
			}
			result, err := loop.Run(ctx, request)
			wantTurns := 1
			if mode == "max_turns" {
				wantTurns = 2
			}
			if err == nil || turns != wantTurns || executions != 1 || result.Retryable {
				t.Fatal("unsafe repair continuation", mode, turns, err)
			}
			if mode == "max_turns" && !errors.Is(err, runtime.ErrLimit) {
				t.Fatal("turn limit changed", err)
			}
			events, err := db.Read(context.Background(), "task", 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, event := range events {
				if event.Kind == runtime.ToolCompleted {
					found = true
					if event.Data.Code != "tool_failed" || event.Data.Effect != want.Effect {
						t.Fatal(fmt.Sprintf("effect changed for %s", mode))
					}
				}
			}
			if found != (mode != "persist") {
				t.Fatal("unexpected tool completion persistence")
			}
		})
	}
}
