package runtime_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

type behaviorExecutor struct {
	executor
	behavior func(string) runtime.ToolBehavior
}

func (e behaviorExecutor) ToolBehavior(name string) runtime.ToolBehavior { return e.behavior(name) }

func TestToolBehaviorDoesNotAuthorizeReplay(t *testing.T) {
	for _, behavior := range []runtime.ToolBehavior{"", runtime.BehaviorReadOnly, runtime.BehaviorIdempotentWrite, runtime.BehaviorNonIdempotentWrite} {
		for _, effect := range []runtime.Effect{runtime.NoEffect, runtime.ConfirmedEffect, runtime.UncertainEffect} {
			t.Run(string(behavior)+"/"+string(effect), func(t *testing.T) {
				db, _ := store(t)
				calls, turns, lookups := 0, 0, 0
				loop := runtime.Loop{Journal: db, Provider: model(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
					turns++
					if turns == 1 {
						return emitCall(emit)
					}
					return &providers.Failure{Code: "unavailable", Retryable: true}
				}), Tools: behaviorExecutor{executor: executor(func(context.Context, providers.ToolCall) (runtime.ToolResult, error) {
					calls++
					return runtime.ToolResult{Content: "fixture", Effect: effect}, nil
				}), behavior: func(string) runtime.ToolBehavior { lookups++; return behavior }}}
				result, err := loop.Run(context.Background(), runRequest())
				if err == nil || result.Retryable || calls != 1 || lookups != 1 {
					t.Fatal("declaration granted retry or repeated execution", err)
				}
				events, err := db.Read(context.Background(), "task", 0, 100)
				if err != nil {
					t.Fatal(err)
				}
				paired := 0
				for _, event := range events {
					if event.Kind == runtime.ToolStarted || event.Kind == runtime.ToolCompleted {
						paired++
						if event.Data.ToolBehavior != behavior {
							t.Fatal("declaration not preserved")
						}
					}
					if event.Kind == runtime.ToolCompleted && event.Data.Effect != effect {
						t.Fatal("declaration erased observed effect")
					}
				}
				if paired != 2 {
					t.Fatal("missing paired behavior evidence")
				}
			})
		}
	}
}

func TestMalformedToolBehaviorStopsBeforeDispatch(t *testing.T) {
	for _, panics := range []bool{false, true} {
		db, _ := store(t)
		calls := 0
		loop := runtime.Loop{Journal: db, Provider: model(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
			return emitCall(emit)
		}), Tools: behaviorExecutor{executor: executor(func(context.Context, providers.ToolCall) (runtime.ToolResult, error) {
			calls++
			return runtime.ToolResult{}, nil
		}), behavior: func(string) runtime.ToolBehavior {
			if panics {
				panic("private declaration error")
			}
			return "unknown"
		}}}
		_, err := loop.Run(context.Background(), runRequest())
		if !errors.Is(err, runtime.ErrTool) || calls != 0 {
			t.Fatal("invalid metadata dispatched")
		}
		events, err := db.Read(context.Background(), "task", 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range events {
			if event.Kind == runtime.ToolStarted {
				t.Fatal("invalid declaration persisted as dispatch")
			}
		}
	}
}
