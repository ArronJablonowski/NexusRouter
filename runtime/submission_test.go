package runtime_test

import (
	"context"
	"errors"
	"testing"

	"darwinrouter/providers"
	"darwinrouter/runtime"
)

func TestSubmissionLeaseGateStopsDispatchAndPreservesReason(t *testing.T) {
	for _, gate := range []runtime.Kind{runtime.TaskStarted, runtime.TurnStarted, runtime.ModelDelta, runtime.TaskCompleted} {
		t.Run(string(gate), func(t *testing.T) {
			var events []runtime.Event
			calls := 0
			loop := runtime.Loop{Journal: journal(func(_ context.Context, seq int64, e runtime.Event) error {
				if e.Kind == gate {
					return runtime.ErrExecutionLeaseLost
				}
				if seq != int64(len(events)) {
					t.Fatal("nonsequential cleanup")
				}
				events = append(events, e)
				return nil
			}), Provider: model(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
				calls++
				return emit(providers.Chunk{Text: "answer", Done: true, FinishReason: "stop"})
			})}
			r := runRequest()
			r.SubmissionID = "submission-1"
			out, err := loop.Run(context.Background(), r)
			if !errors.Is(err, runtime.ErrExecutionLeaseLost) || errors.Is(err, runtime.ErrPersistence) || out.Retryable {
				t.Fatal(out, err)
			}
			if (gate == runtime.TaskStarted || gate == runtime.TurnStarted) && calls != 0 {
				t.Fatal("dispatched without ownership")
			}
			if gate == runtime.TaskStarted {
				if len(events) != 0 {
					t.Fatal("cleanup without task creation")
				}
				return
			}
			if events[0].Data.SubmissionID != r.SubmissionID || events[len(events)-1].Kind != runtime.TaskCanceled || events[len(events)-1].Data.Code != "execution_lease_lost" {
				t.Fatal(events)
			}
		})
	}
}
