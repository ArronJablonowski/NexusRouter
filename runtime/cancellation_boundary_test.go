package runtime_test

import (
	"context"
	"errors"
	"testing"

	"darwinrouter/providers"
	"darwinrouter/runtime"
	"darwinrouter/sessions"
)

func TestCommittedBoundaryCancellationTerminatesDurably(t *testing.T) {
	for _, tc := range []struct {
		kind runtime.Kind
		code string
	}{
		{runtime.TaskStarted, ""}, {runtime.RouteSelected, ""}, {runtime.TurnStarted, ""}, {runtime.ModelDelta, ""},
		{runtime.TurnCompleted, ""}, {runtime.EvaluationRecorded, "deterministic.nonempty_text.v1"}, {runtime.EvaluationRecorded, "deterministic.go_syntax.v1"},
	} {
		t.Run(string(tc.kind)+tc.code, func(t *testing.T) {
			db, _ := store(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			canceled := false
			providerCalls := 0
			loop := runtime.Loop{Journal: journal(func(ctx context.Context, seq int64, event runtime.Event) error {
				if err := db.Append(ctx, seq, event); err != nil {
					return err
				}
				if event.Kind == tc.kind && (tc.code == "" || event.Data.Code == tc.code) {
					canceled = true
					cancel()
				}
				return nil
			}), Provider: model(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
				providerCalls++
				return emit(providers.Chunk{Text: "package main\n", Done: true, FinishReason: "stop"})
			})}
			request := runRequest()
			request.RequireText, request.Validation = true, "go_source"
			request.Route = &runtime.Data{ModelID: "fixture", ProviderID: "fixture"}
			_, err := loop.Run(ctx, request)
			if !canceled || !errors.Is(err, context.Canceled) || errors.Is(err, runtime.ErrPersistence) {
				t.Fatalf("canceled=%v error=%v", canceled, err)
			}
			events, err := db.Read(context.Background(), "task", 0, 100)
			if err != nil || events[len(events)-1].Kind != runtime.TaskCanceled {
				t.Fatalf("missing terminalcleanup: %+v %v", events, err)
			}
			for _, event := range events {
				if event.Kind == runtime.TaskCompleted {
					t.Fatal("canceled task marked completed")
				}
			}
			if tc.kind == runtime.TaskStarted || tc.kind == runtime.RouteSelected || tc.kind == runtime.TurnStarted {
				if providerCalls != 0 {
					t.Fatal("provider dispatched after cancellation")
				}
			}
		})
	}
}

func TestToolBoundaryCancellationPreservesEffects(t *testing.T) {
	for _, kind := range []runtime.Kind{runtime.ToolStarted, runtime.ToolCompleted} {
		t.Run(string(kind), func(t *testing.T) {
			db, _ := store(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			executed := 0
			loop := runtime.Loop{Journal: journal(func(ctx context.Context, seq int64, event runtime.Event) error {
				if err := db.Append(ctx, seq, event); err != nil {
					return err
				}
				if event.Kind == kind {
					cancel()
				}
				return nil
			}), Provider: model(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
				return emitCall(emit)
			}), Tools: executor(func(context.Context, providers.ToolCall) (runtime.ToolResult, error) {
				executed++
				return runtime.ToolResult{Content: "done", Effect: runtime.ConfirmedEffect}, nil
			})}
			_, err := loop.Run(ctx, runRequest())
			if !errors.Is(err, context.Canceled) || errors.Is(err, runtime.ErrPersistence) {
				t.Fatal(err)
			}
			wantExecutions := 0
			wantEffect := runtime.NoEffect
			if kind == runtime.ToolCompleted {
				wantExecutions = 1
				wantEffect = runtime.ConfirmedEffect
			}
			if executed != wantExecutions {
				t.Fatalf("executions=%d want=%d", executed, wantExecutions)
			}
			events, err := db.Read(context.Background(), "task", 0, 100)
			if err != nil || len(events) < 2 || events[len(events)-1].Kind != runtime.TaskCanceled || events[len(events)-2].Kind != runtime.ToolCompleted || events[len(events)-2].Data.Effect != wantEffect {
				t.Fatalf("lost tool disposition: %+v %v", events, err)
			}
			snapshot, err := sessions.Replay(context.Background(), db, "task")
			if err != nil || snapshot.State != "canceled" || len(snapshot.Pending) != 0 || snapshot.UncertainEffects {
				t.Fatalf("unsafe terminal replay: %+v %v", snapshot, err)
			}
		})
	}
}

func TestCanceledBoundaryDoesNotHideActualAppendFailure(t *testing.T) {
	db, _ := store(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	loop := runtime.Loop{Journal: journal(func(ctx context.Context, seq int64, event runtime.Event) error {
		if err := db.Append(ctx, seq, event); err != nil {
			return err
		}
		if event.Kind == runtime.TurnCompleted {
			cancel()
			return errors.New("ambiguous append failure")
		}
		return nil
	}), Provider: model(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
		return emit(providers.Chunk{Text: "answer", Done: true, FinishReason: "stop"})
	})}
	_, err := loop.Run(ctx, runRequest())
	if !errors.Is(err, runtime.ErrPersistence) {
		t.Fatal("append ambiguity lost", err)
	}
	events, err := db.Read(context.Background(), "task", 0, 100)
	if err != nil || events[len(events)-1].Kind != runtime.TurnCompleted {
		t.Fatal("ambiguous append retried with terminal transition", events, err)
	}
}
