package runtime_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

type loopContextEstimator func(context.Context, providers.Request) (int, error)

func (f loopContextEstimator) Estimate(ctx context.Context, r providers.Request) (int, error) {
	return f(ctx, r)
}

func assertContextBudgetFailure(t *testing.T, events []runtime.Event, err error, wantTurns int) {
	t.Helper()
	if !errors.Is(err, runtime.ErrLimit) || err.Error() != runtime.ErrLimit.Error() {
		t.Fatal("estimator failure escaped budget boundary", err)
	}
	if len(events) == 0 || events[len(events)-1].Kind != runtime.TaskFailed || events[len(events)-1].Data.Code != "budget_exhausted" {
		t.Fatal("missing durable budget failure", events)
	}
	turns := 0
	for _, e := range events {
		if e.Kind == runtime.TurnStarted {
			turns++
		}
	}
	if turns != wantTurns {
		t.Fatal("denied turn started", turns, wantTurns)
	}
	body, _ := json.Marshal(events)
	if strings.Contains(string(body), "private-estimator-error") {
		t.Fatal("private estimator error persisted")
	}
}

func TestLoopContextEstimatorDeniesInitialDispatch(t *testing.T) {
	for _, mode := range []string{"larger", "low_floor", "error", "panic"} {
		t.Run(mode, func(t *testing.T) {
			db, _ := store(t)
			r := runRequest()
			r.MaxContextTokens = 2048
			if mode == "low_floor" {
				r.MaxContextTokens = 1
			}
			calls, estimates := 0, 0
			l := runtime.Loop{Journal: db, Provider: model(func(context.Context, providers.Request, func(providers.Chunk) error) error { calls++; return nil }), ContextEstimator: loopContextEstimator(func(context.Context, providers.Request) (int, error) {
				estimates++
				switch mode {
				case "larger":
					return 4096, nil
				case "low_floor":
					return 1, nil
				case "error":
					return 0, errors.New("private-estimator-error")
				default:
					panic("private-estimator-error")
				}
			})}
			_, err := l.Run(context.Background(), r)
			events, readErr := db.Read(context.Background(), r.TaskID, 0, 100)
			if readErr != nil {
				t.Fatal(readErr)
			}
			assertContextBudgetFailure(t, events, err, 0)
			if calls != 0 || estimates != 1 {
				t.Fatal(calls, estimates)
			}
		})
	}
}

func TestLoopContextEstimatorRequiresKnownContextWindow(t *testing.T) {
	for _, limit := range []int{0, -1} {
		db, _ := store(t)
		r := runRequest()
		r.MaxContextTokens = limit
		l := runtime.Loop{Journal: db, Provider: model(func(context.Context, providers.Request, func(providers.Chunk) error) error {
			t.Error("unknown context dispatched provider")
			return nil
		}), ContextEstimator: loopContextEstimator(func(context.Context, providers.Request) (int, error) {
			t.Error("unknown window reached estimator")
			return 1, nil
		})}
		if _, err := l.Run(context.Background(), r); !errors.Is(err, runtime.ErrInvalidRun) {
			t.Fatal(limit, err)
		}
		events, err := db.Read(context.Background(), r.TaskID, 0, 100)
		if err != nil || len(events) != 0 {
			t.Fatal("invalid context window persisted task", events, err)
		}
	}
}

func TestLoopContextEstimatorRechecksGrowingToolHistory(t *testing.T) {
	db, _ := store(t)
	r := runRequest()
	r.MaxContextTokens = 4096
	calls, tools, estimates := 0, 0, 0
	l := runtime.Loop{Journal: db, Provider: model(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
		calls++
		return emitCall(emit)
	}), Tools: executor(func(context.Context, providers.ToolCall) (runtime.ToolResult, error) {
		tools++
		return runtime.ToolResult{Content: "evidence", Effect: runtime.NoEffect}, nil
	}), ContextEstimator: loopContextEstimator(func(_ context.Context, in providers.Request) (int, error) {
		estimates++
		if len(in.Messages) > 1 {
			if len(in.Messages) != 3 || in.Messages[1].ToolCalls[0].ID != "call1" || in.Messages[2].ToolCallID != "call1" || in.Messages[2].Content != "evidence" {
				t.Fatal("estimator did not receive paired history", in.Messages)
			}
			return 4097, nil
		}
		return 1, nil
	})}
	_, err := l.Run(context.Background(), r)
	events, readErr := db.Read(context.Background(), r.TaskID, 0, 100)
	if readErr != nil {
		t.Fatal(readErr)
	}
	assertContextBudgetFailure(t, events, err, 1)
	if calls != 1 || tools != 1 || estimates != 2 {
		t.Fatal(calls, tools, estimates)
	}
	complete := false
	for _, e := range events {
		if e.Kind == runtime.ToolCompleted {
			complete = true
		}
	}
	if !complete {
		t.Fatal("tool evidence lost at denied next-turn boundary")
	}
}

func TestLoopContextEstimatorGuardsSteeringBeforeCommit(t *testing.T) {
	for _, mode := range []string{"larger", "low_floor", "error", "panic"} {
		t.Run(mode, func(t *testing.T) {
			journal := &steeringFixture{}
			journal.queue("new guidance")
			r := runRequest()
			r.MaxContextTokens = 2048
			if mode == "low_floor" {
				r.MaxContextTokens = 1
			}
			calls, estimates := 0, 0
			l := runtime.Loop{Journal: journal, Steering: journal, Provider: model(func(context.Context, providers.Request, func(providers.Chunk) error) error { calls++; return nil }), ContextEstimator: loopContextEstimator(func(_ context.Context, in providers.Request) (int, error) {
				estimates++
				if len(in.Messages) != 2 || in.Messages[1].Content != "new guidance" {
					t.Fatal("candidate guidance absent", in.Messages)
				}
				switch mode {
				case "larger":
					return 4096, nil
				case "low_floor":
					return 1, nil
				case "error":
					return 0, errors.New("private-estimator-error")
				default:
					panic("private-estimator-error")
				}
			})}
			_, err := l.Run(context.Background(), r)
			assertContextBudgetFailure(t, journal.events, err, 0)
			if calls != 0 || estimates != 1 || len(journal.pending) != 1 {
				t.Fatal(calls, estimates, len(journal.pending))
			}
			for _, e := range journal.events {
				if e.Kind == runtime.SteeringApplied {
					t.Fatal("denied steering committed")
				}
			}
		})
	}
}
