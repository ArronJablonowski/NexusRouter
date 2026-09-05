package runtime_test

import (
	"context"
	"errors"
	"testing"

	"darwinrouter/providers"
	"darwinrouter/runtime"
)

func TestDurableCancellationGateBoundaries(t *testing.T) {
	for _, gate := range []runtime.Kind{runtime.RouteSelected, runtime.TurnStarted, runtime.ModelDelta, runtime.TurnCompleted, runtime.EvaluationRecorded, runtime.TaskCompleted, runtime.TaskFailed} {
		t.Run(string(gate), func(t *testing.T) {
			var events []runtime.Event
			loop := runtime.Loop{Journal: journal(func(ctx context.Context, seq int64, e runtime.Event) error {
				if e.Kind == gate {
					return runtime.ErrCancellationRequested
				}
				if seq != int64(len(events)) || ctx.Err() != nil {
					t.Fatal("cleanup was not bounded and sequential", seq, ctx.Err())
				}
				events = append(events, e)
				return nil
			}), Provider: model(func(ctx context.Context, r providers.Request, emit func(providers.Chunk) error) error {
				if gate == runtime.TaskFailed {
					return errors.New("provider failure")
				}
				return emit(providers.Chunk{Text: "answer", Done: true, FinishReason: "stop"})
			})}
			r := runRequest()
			r.RequireText = true
			r.Route = &runtime.Data{}
			out, err := loop.Run(context.Background(), r)
			if !errors.Is(err, context.Canceled) || errors.Is(err, runtime.ErrPersistence) || out.Retryable {
				t.Fatal(out, err)
			}
			if len(events) < 2 || events[len(events)-1].Kind != runtime.TaskCanceled {
				t.Fatal("missing durable cancellation", events)
			}
		})
	}
}

func TestDurableCancellationDoesNotRetryAmbiguousCleanup(t *testing.T) {
	var calls []runtime.Kind
	loop := runtime.Loop{Journal: journal(func(_ context.Context, _ int64, e runtime.Event) error {
		calls = append(calls, e.Kind)
		if e.Kind == runtime.TurnStarted {
			return runtime.ErrCancellationRequested
		}
		if e.Kind == runtime.TaskCanceled {
			return errors.New("ambiguous commit")
		}
		return nil
	}), Provider: model(func(context.Context, providers.Request, func(providers.Chunk) error) error {
		t.Fatal("dispatch after cancellation")
		return nil
	})}
	_, err := loop.Run(context.Background(), runRequest())
	if !errors.Is(err, runtime.ErrPersistence) || len(calls) != 3 {
		t.Fatal(calls, err)
	}
}

func TestDurableCancellationRetainsCompletedToolEffect(t *testing.T) {
	db, _ := store(t)
	loop := runtime.Loop{Journal: db,
		Provider: model(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
			return emitCall(emit)
		}),
		Tools: executor(func(ctx context.Context, _ providers.ToolCall) (runtime.ToolResult, error) {
			if _, err := db.RequestCancellation(ctx, "task"); err != nil {
				t.Fatal(err)
			}
			return runtime.ToolResult{Content: "effect recorded", Effect: runtime.ConfirmedEffect}, nil
		}),
	}
	out, err := loop.Run(context.Background(), runRequest())
	if !errors.Is(err, context.Canceled) || out.Retryable {
		t.Fatal(out, err)
	}
	events, err := db.Read(context.Background(), "task", 0, 100)
	if err != nil || len(events) < 2 {
		t.Fatal(events, err)
	}
	last := events[len(events)-2:]
	if last[0].Kind != runtime.ToolCompleted || last[0].Data.Effect != runtime.ConfirmedEffect || last[1].Kind != runtime.TaskCanceled {
		t.Fatal("lost completed effect", last)
	}
}
