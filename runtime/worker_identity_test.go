package runtime_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

type identityJournal struct {
	events []runtime.Event
}

func (j *identityJournal) Append(_ context.Context, sequence int64, event runtime.Event) error {
	if event.Sequence != sequence+1 || event.Validate() != nil {
		return errors.New("invalid event")
	}
	clone, err := event.Clone()
	if err != nil {
		return err
	}
	j.events = append(j.events, clone)
	return nil
}

func TestLoopPropagatesTrustedWorkerIdentityToEveryEvent(t *testing.T) {
	const workerID = "workboard-worker-capability"
	j := &identityJournal{}
	turns := 0
	loop := runtime.Loop{
		Journal: j,
		Provider: model(func(_ context.Context, request providers.Request, emit func(providers.Chunk) error) error {
			turns++
			if turns == 1 {
				return emitCall(emit)
			}
			if len(request.Messages) != 3 || request.Messages[2].ToolCallID != "call1" {
				return errors.New("tool result pair was not retained")
			}
			return emit(providers.Chunk{Text: "answer", Done: true, FinishReason: "stop"})
		}),
		Tools: executor(func(context.Context, providers.ToolCall) (runtime.ToolResult, error) {
			return runtime.ToolResult{Content: "evidence", Effect: runtime.NoEffect}, nil
		}),
	}
	r := runRequest()
	r.WorkerID = workerID
	r.RequireText = true
	r.Route = &runtime.Data{ModelID: "fixture", ProviderID: "fixture"}
	result, err := loop.Run(context.Background(), r)
	if err != nil || result.Text != "answer" || turns != 2 {
		t.Fatalf("result=%+v turns=%d err=%v", result, turns, err)
	}
	want := []runtime.Kind{
		runtime.TaskStarted, runtime.RouteSelected,
		runtime.TurnStarted, runtime.TurnCompleted,
		runtime.ToolStarted, runtime.ToolCompleted,
		runtime.TurnStarted, runtime.ModelDelta, runtime.TurnCompleted,
		runtime.EvaluationRecorded, runtime.TaskCompleted,
	}
	if len(j.events) != len(want) {
		t.Fatalf("events=%+v", j.events)
	}
	for i, event := range j.events {
		if event.Kind != want[i] || event.WorkerID != workerID {
			t.Fatalf("event %d lost host identity: %+v", i, event)
		}
	}
}

func TestLoopPropagatesTrustedWorkerIdentityThroughErrorTerminals(t *testing.T) {
	for _, terminal := range []runtime.Kind{runtime.TaskFailed, runtime.TaskCanceled} {
		t.Run(string(terminal), func(t *testing.T) {
			const workerID = "workboard-worker-failure"
			j := &identityJournal{}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			loop := runtime.Loop{
				Journal: j,
				Provider: model(func(context.Context, providers.Request, func(providers.Chunk) error) error {
					if terminal == runtime.TaskCanceled {
						cancel()
						return ctx.Err()
					}
					return errors.New("provider unavailable")
				}),
			}
			r := runRequest()
			r.WorkerID = workerID
			if _, err := loop.Run(ctx, r); err == nil {
				t.Fatal("error terminal reported success")
			}
			want := []runtime.Kind{runtime.TaskStarted, runtime.TurnStarted, terminal}
			if len(j.events) != len(want) {
				t.Fatalf("events=%+v", j.events)
			}
			for i, event := range j.events {
				if event.Kind != want[i] || event.WorkerID != workerID {
					t.Fatalf("event %d lost host identity: %+v", i, event)
				}
			}
		})
	}
}

func TestLoopWorkerIdentityAdmissionAndRootCompatibility(t *testing.T) {
	for name, workerID := range map[string]string{
		"space":     "worker identity",
		"newline":   "worker\nidentity",
		"non_ascii": "worker-é",
		"too_long":  strings.Repeat("w", 129),
		"delete":    "worker\x7fidentity",
	} {
		t.Run(name, func(t *testing.T) {
			j := &identityJournal{}
			dispatches := 0
			loop := runtime.Loop{Journal: j, Provider: model(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
				dispatches++
				return emit(providers.Chunk{Text: "unexpected", Done: true, FinishReason: "stop"})
			})}
			r := runRequest()
			r.WorkerID = workerID
			if result, err := loop.Run(context.Background(), r); !errors.Is(err, runtime.ErrInvalidRun) || result != (runtime.Result{}) {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if dispatches != 0 || len(j.events) != 0 {
				t.Fatalf("invalid identity reached execution: dispatches=%d events=%+v", dispatches, j.events)
			}
		})
	}

	j := &identityJournal{}
	loop := runtime.Loop{Journal: j, Provider: model(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
		return emit(providers.Chunk{Text: "root answer", Done: true, FinishReason: "stop"})
	})}
	result, err := loop.Run(context.Background(), runRequest())
	if err != nil || result.Text != "root answer" || len(j.events) == 0 {
		t.Fatalf("result=%+v events=%+v err=%v", result, j.events, err)
	}
	for i, event := range j.events {
		if event.WorkerID != "" {
			t.Fatalf("root event %d acquired worker identity: %+v", i, event)
		}
	}
}
