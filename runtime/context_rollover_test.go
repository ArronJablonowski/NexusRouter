package runtime_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

type rolloverModel struct {
	stream   model
	check    func(context.Context, providers.Request, providers.Request) error
	activate func(context.Context, providers.Request) error
}

type rolloverJournal struct {
	*steeringFixture
	plans       []runtime.ContextCompactionPlan
	atomicCalls int
	atomic      func(context.Context, int64, runtime.Event, runtime.ContextCompactionPlan) error
}

func newRolloverJournal() *rolloverJournal {
	return &rolloverJournal{steeringFixture: &steeringFixture{}}
}

func (j *rolloverJournal) AppendContextCompaction(ctx context.Context, expected int64, event runtime.Event, plan runtime.ContextCompactionPlan) error {
	j.atomicCalls++
	if j.atomic != nil {
		return j.atomic(ctx, expected, event, plan)
	}
	clone, err := event.Clone()
	if err != nil {
		return err
	}
	j.events = append(j.events, clone)
	j.plans = append(j.plans, plan)
	return nil
}

func (m *rolloverModel) Stream(ctx context.Context, request providers.Request, emit func(providers.Chunk) error) error {
	return m.stream(ctx, request, emit)
}

func (*rolloverModel) Models(context.Context) ([]string, error) { return []string{"fixture"}, nil }

func (m *rolloverModel) CheckContextRollover(ctx context.Context, current, replacement providers.Request) error {
	return m.check(ctx, current, replacement)
}

func (m *rolloverModel) ActivateContextRollover(ctx context.Context, base providers.Request) error {
	return m.activate(ctx, base)
}

func rolloverRunRequest(t *testing.T) runtime.RunRequest {
	t.Helper()
	r := runRequest()
	r.ParentTaskID = "source-task"
	r.MaxContextTokens = 4000
	r.CompactionPlan = plannedMidTaskCompaction(t, r)
	r.RequireContextRollover = true
	return r
}

func TestRequiredContextRolloverOrdersCheckCommitActivateBeforeDispatch(t *testing.T) {
	r := rolloverRunRequest(t)
	j := newRolloverJournal()
	order := []string{}
	calls := 0
	provider := &rolloverModel{
		stream: func(_ context.Context, request providers.Request, emit func(providers.Chunk) error) error {
			calls++
			order = append(order, "stream")
			if calls == 1 {
				j.queue("continue")
				return emit(providers.Chunk{Text: "first", Done: true, FinishReason: "stop"})
			}
			if len(request.Messages) != 3 || request.Messages[0].Content != "approved summary" || request.Messages[1].Content != "first" || request.Messages[2].Content != "continue" {
				t.Fatalf("unexpected activated request: %+v", request.Messages)
			}
			return emit(providers.Chunk{Text: "done", Done: true, FinishReason: "stop"})
		},
		check: func(_ context.Context, current, replacement providers.Request) error {
			order = append(order, "check")
			if len(current.Messages) != 2 || current.Messages[1].Content != "first" || len(replacement.Messages) != 3 || replacement.Messages[0].Content != "approved summary" || replacement.Messages[2].Content != "continue" {
				t.Fatalf("check did not receive exact prospective requests: current=%+v replacement=%+v", current.Messages, replacement.Messages)
			}
			return nil
		},
		activate: func(_ context.Context, base providers.Request) error {
			order = append(order, "activate")
			if len(base.Messages) != 2 || base.Messages[0].Content != "approved summary" || base.Messages[1].Content != "first" {
				t.Fatalf("activation did not receive compacted base: %+v", base.Messages)
			}
			return nil
		},
	}
	j.atomic = func(ctx context.Context, expected int64, event runtime.Event, plan runtime.ContextCompactionPlan) error {
		order = append(order, "commit")
		clone, err := event.Clone()
		if err != nil {
			return err
		}
		j.events = append(j.events, clone)
		j.plans = append(j.plans, plan)
		return nil
	}
	loop := runtime.Loop{ContextEstimator: loopContextEstimator(growingContextEstimator), Journal: j, Steering: j, Provider: provider}
	result, err := loop.Run(context.Background(), r)
	if err != nil || result.Text != "done" || calls != 2 {
		t.Fatal(result, calls, err)
	}
	if got := strings.Join(order, ","); got != "stream,check,commit,activate,stream" {
		t.Fatal("invalid rollover order", got)
	}
}

func TestRequiredContextRolloverRejectsPausedToolTurn(t *testing.T) {
	r := rolloverRunRequest(t)
	j := &compactionJournal{}
	calls, checks, activates := 0, 0, 0
	provider := &rolloverModel{
		stream: func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
			calls++
			return emitCall(emit)
		},
		check:    func(context.Context, providers.Request, providers.Request) error { checks++; return nil },
		activate: func(context.Context, providers.Request) error { activates++; return nil },
	}
	loop := runtime.Loop{
		ContextEstimator: loopContextEstimator(growingContextEstimator), Journal: j, Provider: provider,
		Tools: executor(func(context.Context, providers.ToolCall) (runtime.ToolResult, error) {
			return runtime.ToolResult{Content: "evidence", Effect: runtime.NoEffect}, nil
		}),
	}
	_, err := loop.Run(context.Background(), r)
	if !errors.Is(err, runtime.ErrContextOverflow) || calls != 1 || checks != 0 || activates != 0 || j.atomicCalls != 0 {
		t.Fatal(err, calls, checks, activates, j.atomicCalls)
	}
}

func TestRequiredContextRolloverFailuresStopWithoutRedispatch(t *testing.T) {
	for _, phase := range []string{"check_error", "check_panic", "activate_error", "activate_panic"} {
		t.Run(phase, func(t *testing.T) {
			r := rolloverRunRequest(t)
			j := newRolloverJournal()
			calls, checks, activates := 0, 0, 0
			provider := &rolloverModel{
				stream: func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
					calls++
					j.queue("continue")
					return emit(providers.Chunk{Text: "first", Done: true, FinishReason: "stop"})
				},
				check: func(context.Context, providers.Request, providers.Request) error {
					checks++
					if phase == "check_panic" {
						panic("private check panic")
					}
					if phase == "check_error" {
						return errors.New("private check error")
					}
					return nil
				},
				activate: func(context.Context, providers.Request) error {
					activates++
					if phase == "activate_panic" {
						panic("private activate panic")
					}
					return errors.New("private activate error")
				},
			}
			loop := runtime.Loop{ContextEstimator: loopContextEstimator(growingContextEstimator), Journal: j, Steering: j, Provider: provider}
			_, err := loop.Run(context.Background(), r)
			if !errors.Is(err, runtime.ErrProvider) || calls != 1 || checks != 1 {
				t.Fatal(err, calls, checks, activates)
			}
			wantAtomic, wantActivate := 0, 0
			if strings.HasPrefix(phase, "activate") {
				wantAtomic, wantActivate = 1, 1
			}
			if j.atomicCalls != wantAtomic || activates != wantActivate || strings.Contains(err.Error(), "private") {
				t.Fatal(err, j.atomicCalls, activates)
			}
		})
	}
}

func TestRequiredContextRolloverCancellationAfterCommitActivatesOnceAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := rolloverRunRequest(t)
	j := newRolloverJournal()
	calls, checks, activates := 0, 0, 0
	provider := &rolloverModel{
		stream: func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
			calls++
			j.queue("continue")
			return emit(providers.Chunk{Text: "first", Done: true, FinishReason: "stop"})
		},
		check: func(context.Context, providers.Request, providers.Request) error { checks++; return nil },
		activate: func(hookCtx context.Context, _ providers.Request) error {
			activates++
			return hookCtx.Err()
		},
	}
	j.atomic = func(hookCtx context.Context, expected int64, event runtime.Event, plan runtime.ContextCompactionPlan) error {
		clone, err := event.Clone()
		if err != nil {
			return err
		}
		j.events = append(j.events, clone)
		j.plans = append(j.plans, plan)
		cancel()
		return nil
	}
	loop := runtime.Loop{ContextEstimator: loopContextEstimator(growingContextEstimator), Journal: j, Steering: j, Provider: provider}
	_, err := loop.Run(ctx, r)
	if !errors.Is(err, context.Canceled) || calls != 1 || checks != 1 || activates != 1 || j.atomicCalls != 1 {
		t.Fatal(err, calls, checks, activates, j.atomicCalls)
	}
}

func TestRequiredContextRolloverRequiresProviderAndExactPlanBeforePersistence(t *testing.T) {
	for _, mutate := range []func(*runtime.RunRequest){
		func(r *runtime.RunRequest) {
			r.CompactionPlan = nil
			r.ApprovedCompaction = approvedMidTaskCompaction(*r)
		},
		func(r *runtime.RunRequest) {},
	} {
		r := rolloverRunRequest(t)
		mutate(&r)
		loop := runtime.Loop{
			Journal: journal(func(context.Context, int64, runtime.Event) error { t.Fatal("invalid rollover persisted"); return nil }),
			Provider: model(func(context.Context, providers.Request, func(providers.Chunk) error) error {
				t.Fatal("invalid rollover dispatched")
				return nil
			}),
		}
		if _, err := loop.Run(context.Background(), r); !errors.Is(err, runtime.ErrInvalidRun) {
			t.Fatal(err)
		}
	}
}
