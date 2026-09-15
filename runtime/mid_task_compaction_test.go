package runtime_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func approvedMidTaskCompaction(r runtime.RunRequest) *runtime.ApprovedCompaction {
	checkpoint := compactionFixture()
	checkpoint.SummaryAttemptID = "summary-attempt"
	checkpoint.SummaryReviewID = "summary-review"
	return &runtime.ApprovedCompaction{
		Compaction:        checkpoint,
		OriginalPrefix:    append([]providers.Message(nil), r.Inference.Messages...),
		ReplacementPrefix: []providers.Message{{Role: "user", Content: "approved summary"}},
	}
}

func plannedMidTaskCompaction(t *testing.T, r runtime.RunRequest) *runtime.ContextCompactionPlan {
	t.Helper()
	plan := compactionPlanFixture(t)
	plan.OriginalPrefix = append([]providers.Message(nil), r.Inference.Messages...)
	plan.ReplacementPrefix = []providers.Message{{Role: "user", Content: "approved summary"}}
	plan.LiveSuffixBoundary = len(plan.OriginalPrefix)
	sealed, err := runtime.SealContextCompactionPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	return &sealed
}

type compactionJournal struct {
	events      []runtime.Event
	plans       []runtime.ContextCompactionPlan
	atomicCalls int
	atomic      func(context.Context, int64, runtime.Event, runtime.ContextCompactionPlan) error
}

func (j *compactionJournal) Append(_ context.Context, _ int64, event runtime.Event) error {
	clone, err := event.Clone()
	if err != nil {
		return err
	}
	j.events = append(j.events, clone)
	return nil
}

func (j *compactionJournal) AppendContextCompaction(ctx context.Context, expected int64, event runtime.Event, plan runtime.ContextCompactionPlan) error {
	j.atomicCalls++
	if j.atomic != nil {
		return j.atomic(ctx, expected, event, plan)
	}
	if expected != int64(len(j.events)) || event.Sequence != expected+1 {
		return errors.New("invalid atomic append sequence")
	}
	clone, err := event.Clone()
	if err != nil {
		return err
	}
	j.events = append(j.events, clone)
	j.plans = append(j.plans, plan)
	return nil
}

func growingContextEstimator(_ context.Context, request providers.Request) (int, error) {
	if len(request.Messages) == 1 {
		return 2000, nil
	}
	if request.Messages[0].Content == "approved summary" {
		return 3000, nil
	}
	return 8000, nil
}

func TestApprovedCompactionActivatesBeforeLaterToolTurn(t *testing.T) {
	r := runRequest()
	r.ParentTaskID = "parent"
	r.MaxContextTokens = 4000
	r.ApprovedCompaction = approvedMidTaskCompaction(r)
	events := []runtime.Event{}
	providerCalls := 0
	loop := runtime.Loop{
		ContextEstimator: loopContextEstimator(growingContextEstimator),
		Journal: journal(func(_ context.Context, _ int64, event runtime.Event) error {
			clone, err := event.Clone()
			if err != nil {
				return err
			}
			events = append(events, clone)
			return nil
		}),
		Provider: model(func(_ context.Context, request providers.Request, emit func(providers.Chunk) error) error {
			providerCalls++
			if providerCalls == 1 {
				return emitCall(emit)
			}
			if len(request.Messages) != 3 || request.Messages[0].Content != "approved summary" ||
				len(request.Messages[1].ToolCalls) != 1 || request.Messages[1].ToolCalls[0].ID != "call1" ||
				request.Messages[2].Role != "tool" || request.Messages[2].ToolCallID != "call1" || request.Messages[2].Content != "evidence" {
				t.Fatalf("compaction did not retain the live tool pair: %+v", request.Messages)
			}
			return emit(providers.Chunk{Text: "done", Done: true, FinishReason: "stop"})
		}),
		Tools: executor(func(context.Context, providers.ToolCall) (runtime.ToolResult, error) {
			return runtime.ToolResult{Content: "evidence", Effect: runtime.NoEffect}, nil
		}),
	}
	result, err := loop.Run(context.Background(), r)
	if err != nil || result.Text != "done" || providerCalls != 2 {
		t.Fatal(result, providerCalls, err)
	}
	compactions := 0
	for i, event := range events {
		if event.Kind != runtime.ContextCompacted {
			continue
		}
		compactions++
		if i == 0 || i+1 >= len(events) || events[i-1].Kind != runtime.ToolCompleted || events[i+1].Kind != runtime.TurnStarted ||
			event.TurnID != "" || event.AttemptID != "" || event.Data.ReplacedMessages != 1 ||
			len(event.Data.Messages) != 1 || event.Data.Messages[0].Content != "approved summary" {
			t.Fatalf("invalid durable activation boundary: %d %+v", i, event)
		}
	}
	if compactions != 1 {
		t.Fatal("activation was not exactly once", compactions)
	}
}

func TestCompactionPlanActivatesThroughAtomicJournal(t *testing.T) {
	r := runRequest()
	r.ParentTaskID = "source-task"
	r.MaxContextTokens = 4000
	r.CompactionPlan = plannedMidTaskCompaction(t, r)
	planDigest := r.CompactionPlan.PlanDigest
	j := &compactionJournal{}
	providerCalls := 0
	loop := runtime.Loop{
		ContextEstimator: loopContextEstimator(growingContextEstimator), Journal: j,
		Provider: model(func(_ context.Context, request providers.Request, emit func(providers.Chunk) error) error {
			providerCalls++
			if providerCalls == 1 {
				r.CompactionPlan.ReplacementPrefix[0].Content = "caller mutation"
				return emitCall(emit)
			}
			if len(request.Messages) != 3 || request.Messages[0].Content != "approved summary" ||
				len(request.Messages[1].ToolCalls) != 1 || request.Messages[2].ToolCallID != "call1" {
				t.Fatalf("plan activation lost live suffix: %+v", request.Messages)
			}
			return emit(providers.Chunk{Text: "done", Done: true, FinishReason: "stop"})
		}),
		Tools: executor(func(context.Context, providers.ToolCall) (runtime.ToolResult, error) {
			return runtime.ToolResult{Content: "evidence", Effect: runtime.NoEffect}, nil
		}),
	}
	result, err := loop.Run(context.Background(), r)
	if err != nil || result.Text != "done" || providerCalls != 2 || j.atomicCalls != 1 || len(j.plans) != 1 {
		t.Fatal(result, providerCalls, j.atomicCalls, len(j.plans), err)
	}
	if j.plans[0].PlanDigest != planDigest || j.plans[0].Validate() != nil || j.plans[0].ReplacementPrefix[0].Content != "approved summary" {
		t.Fatal("runtime did not own the atomic plan")
	}
	compactions := 0
	for _, event := range j.events {
		if event.Kind == runtime.ContextCompacted {
			compactions++
		}
	}
	if compactions != 1 {
		t.Fatal("atomic path did not persist exactly one compaction", compactions)
	}
}

func TestCompactionPlanRequiresAtomicJournalBeforePersistence(t *testing.T) {
	r := runRequest()
	r.ParentTaskID = "source-task"
	r.MaxContextTokens = 4000
	r.CompactionPlan = plannedMidTaskCompaction(t, r)
	loop := runtime.Loop{
		Journal: journal(func(context.Context, int64, runtime.Event) error {
			t.Fatal("plan without atomic journal persisted")
			return nil
		}),
		Provider: model(func(context.Context, providers.Request, func(providers.Chunk) error) error {
			t.Fatal("plan without atomic journal dispatched")
			return nil
		}),
	}
	if _, err := loop.Run(context.Background(), r); !errors.Is(err, runtime.ErrInvalidRun) {
		t.Fatal(err)
	}
}

func TestCompactionPlanAtomicFailureIsAmbiguousAndStopsDispatch(t *testing.T) {
	for _, mode := range []string{"error", "panic"} {
		t.Run(mode, func(t *testing.T) {
			r := runRequest()
			r.ParentTaskID = "source-task"
			r.MaxContextTokens = 4000
			r.CompactionPlan = plannedMidTaskCompaction(t, r)
			j := &compactionJournal{atomic: func(context.Context, int64, runtime.Event, runtime.ContextCompactionPlan) error {
				if mode == "panic" {
					panic("unknown commit state")
				}
				return errors.New("unknown commit state")
			}}
			providerCalls := 0
			loop := runtime.Loop{
				ContextEstimator: loopContextEstimator(growingContextEstimator), Journal: j,
				Provider: model(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
					providerCalls++
					return emitCall(emit)
				}),
				Tools: executor(func(context.Context, providers.ToolCall) (runtime.ToolResult, error) {
					return runtime.ToolResult{Content: "evidence", Effect: runtime.NoEffect}, nil
				}),
			}
			_, err := loop.Run(context.Background(), r)
			if !errors.Is(err, runtime.ErrPersistence) || providerCalls != 1 || j.atomicCalls != 1 {
				t.Fatal(err, providerCalls, j.atomicCalls)
			}
			for _, event := range j.events {
				if event.Kind == runtime.ContextCompacted || event.Kind == runtime.TaskFailed {
					t.Fatal("ambiguous activation invented durable state", j.events)
				}
			}
		})
	}
}

func TestCompactionPlanRejectsMutuallyExclusiveLegacyCompaction(t *testing.T) {
	r := runRequest()
	r.ParentTaskID = "source-task"
	r.MaxContextTokens = 4000
	r.CompactionPlan = plannedMidTaskCompaction(t, r)
	r.ApprovedCompaction = approvedMidTaskCompaction(r)
	loop := runtime.Loop{Journal: &compactionJournal{}, Provider: model(func(context.Context, providers.Request, func(providers.Chunk) error) error {
		t.Fatal("mutually exclusive compactions dispatched")
		return nil
	})}
	if _, err := loop.Run(context.Background(), r); !errors.Is(err, runtime.ErrInvalidRun) {
		t.Fatal(err)
	}
}

func TestApprovedCompactionUsesLegacyAppendOnAtomicJournal(t *testing.T) {
	r := runRequest()
	r.ParentTaskID = "parent"
	r.MaxContextTokens = 4000
	r.ApprovedCompaction = approvedMidTaskCompaction(r)
	j := &compactionJournal{}
	providerCalls := 0
	loop := runtime.Loop{
		ContextEstimator: loopContextEstimator(growingContextEstimator), Journal: j,
		Provider: model(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
			providerCalls++
			if providerCalls == 1 {
				return emitCall(emit)
			}
			return emit(providers.Chunk{Text: "done", Done: true, FinishReason: "stop"})
		}),
		Tools: executor(func(context.Context, providers.ToolCall) (runtime.ToolResult, error) {
			return runtime.ToolResult{Content: "evidence", Effect: runtime.NoEffect}, nil
		}),
	}
	if _, err := loop.Run(context.Background(), r); err != nil || j.atomicCalls != 0 {
		t.Fatal(err, j.atomicCalls)
	}
}

func TestApprovedCompactionPrecedesProspectiveSteering(t *testing.T) {
	j := &steeringFixture{}
	r := runRequest()
	r.ParentTaskID = "parent"
	r.MaxContextTokens = 4000
	r.ApprovedCompaction = approvedMidTaskCompaction(r)
	providerCalls := 0
	loop := runtime.Loop{
		ContextEstimator: loopContextEstimator(growingContextEstimator), Journal: j, Steering: j,
		Provider: model(func(_ context.Context, request providers.Request, emit func(providers.Chunk) error) error {
			providerCalls++
			if providerCalls == 1 {
				j.queue("new guidance")
				return emit(providers.Chunk{Text: "first", Done: true, FinishReason: "stop"})
			}
			if len(request.Messages) != 3 || request.Messages[0].Content != "approved summary" || request.Messages[1].Content != "first" || request.Messages[2].Content != "new guidance" {
				t.Fatalf("steering or live suffix lost: %+v", request.Messages)
			}
			return emit(providers.Chunk{Text: "final", Done: true, FinishReason: "stop"})
		}),
	}
	if _, err := loop.Run(context.Background(), r); err != nil || providerCalls != 2 {
		t.Fatal(providerCalls, err)
	}
	want := []runtime.Kind{runtime.TurnCompleted, runtime.ContextCompacted, runtime.SteeringApplied, runtime.TurnStarted}
	for i := 0; i+len(want) <= len(j.events); i++ {
		matched := true
		for offset := range want {
			matched = matched && j.events[i+offset].Kind == want[offset]
		}
		if matched {
			return
		}
	}
	t.Fatal("activation did not precede steering and dispatch", j.events)
}

func TestApprovedCompactionFailureIsFailClosed(t *testing.T) {
	for _, mode := range []string{"persistence", "too_large", "estimator_error"} {
		t.Run(mode, func(t *testing.T) {
			r := runRequest()
			r.ParentTaskID = "parent"
			r.MaxContextTokens = 4000
			r.ApprovedCompaction = approvedMidTaskCompaction(r)
			events := []runtime.Event{}
			providerCalls := 0
			estimator := loopContextEstimator(func(ctx context.Context, request providers.Request) (int, error) {
				if mode == "estimator_error" && len(request.Messages) > 1 && request.Messages[0].Content == "approved summary" {
					return 0, errors.New("private estimator error")
				}
				if mode == "too_large" && len(request.Messages) > 1 && request.Messages[0].Content == "approved summary" {
					return 6000, nil
				}
				return growingContextEstimator(ctx, request)
			})
			loop := runtime.Loop{
				ContextEstimator: estimator,
				Journal: journal(func(_ context.Context, _ int64, event runtime.Event) error {
					if mode == "persistence" && event.Kind == runtime.ContextCompacted {
						return errors.New("disk unavailable")
					}
					events = append(events, event)
					return nil
				}),
				Provider: model(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
					providerCalls++
					return emitCall(emit)
				}),
				Tools: executor(func(context.Context, providers.ToolCall) (runtime.ToolResult, error) {
					return runtime.ToolResult{Content: "evidence", Effect: runtime.NoEffect}, nil
				}),
			}
			_, err := loop.Run(context.Background(), r)
			want := runtime.ErrContextOverflow
			if mode == "persistence" {
				want = runtime.ErrPersistence
			} else if mode == "estimator_error" {
				want = providers.ErrContextEstimate
			}
			if !errors.Is(err, want) || providerCalls != 1 {
				t.Fatal(err, providerCalls)
			}
			for _, event := range events {
				if event.Kind == runtime.ContextCompacted || event.Sequence > 5 && event.Kind == runtime.TurnStarted {
					t.Fatal("failed activation escaped its durable boundary", events)
				}
			}
			body := strings.Builder{}
			for _, event := range events {
				body.WriteString(event.Data.Code)
			}
			if strings.Contains(body.String(), "private estimator error") {
				t.Fatal("private estimator error escaped")
			}
		})
	}
}

func TestApprovedCompactionJournalLimitUsesReservedTerminal(t *testing.T) {
	r := runRequest()
	r.ParentTaskID = "parent"
	r.MaxContextTokens = 4000
	r.ApprovedCompaction = approvedMidTaskCompaction(r)
	events := []runtime.Event{}
	rejected := false
	providerCalls := 0
	loop := runtime.Loop{
		ContextEstimator: loopContextEstimator(growingContextEstimator),
		Journal: journal(func(_ context.Context, _ int64, event runtime.Event) error {
			if !rejected && len(events) > 0 && events[len(events)-1].Kind == runtime.ContextCompacted && event.Kind == runtime.TurnStarted {
				rejected = true
				return runtime.ErrJournalLimit
			}
			events = append(events, event)
			return nil
		}),
		Provider: model(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
			providerCalls++
			return emitCall(emit)
		}),
		Tools: executor(func(context.Context, providers.ToolCall) (runtime.ToolResult, error) {
			return runtime.ToolResult{Content: "evidence", Effect: runtime.NoEffect}, nil
		}),
	}
	_, err := loop.Run(context.Background(), r)
	if !errors.Is(err, runtime.ErrJournalLimit) || !rejected || providerCalls != 1 || len(events) == 0 {
		t.Fatal("definitive journal limit did not stop redispatch", err, rejected, providerCalls, events)
	}
	last := events[len(events)-1]
	if last.Kind != runtime.TaskFailed || last.Data.Code != "journal_exhausted" || last.Sequence != int64(len(events)) {
		t.Fatal("reserved terminal was not committed", last, events)
	}
}

func TestApprovedCompactionCannotActivateOnInitialTurnOrTwice(t *testing.T) {
	for _, mode := range []string{"initial", "second_overflow"} {
		t.Run(mode, func(t *testing.T) {
			r := runRequest()
			r.ParentTaskID = "parent"
			r.MaxContextTokens = 4000
			r.ApprovedCompaction = approvedMidTaskCompaction(r)
			calls, compactions := 0, 0
			loop := runtime.Loop{Journal: journal(func(_ context.Context, _ int64, event runtime.Event) error {
				if event.Kind == runtime.ContextCompacted {
					compactions++
				}
				return nil
			}), Provider: model(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
				calls++
				call := providers.ToolCall{ID: "call" + string(rune('0'+calls)), Name: "lookup", Arguments: []byte(`{}`)}
				if err := emit(providers.Chunk{ToolCall: &call}); err != nil {
					return err
				}
				return emit(providers.Chunk{Done: true, FinishReason: "tool_calls"})
			}), Tools: executor(func(context.Context, providers.ToolCall) (runtime.ToolResult, error) {
				return runtime.ToolResult{Content: "evidence", Effect: runtime.NoEffect}, nil
			})}
			loop.ContextEstimator = loopContextEstimator(func(_ context.Context, request providers.Request) (int, error) {
				if mode == "initial" {
					return 8000, nil
				}
				if len(request.Messages) == 1 {
					return 2000, nil
				}
				if request.Messages[0].Content == "approved summary" && len(request.Messages) == 3 {
					return 3000, nil
				}
				return 8000, nil
			})
			_, err := loop.Run(context.Background(), r)
			if mode == "initial" {
				if !errors.Is(err, runtime.ErrContextOverflow) || calls != 0 || compactions != 0 {
					t.Fatal(err, calls, compactions)
				}
				return
			}
			if !errors.Is(err, runtime.ErrContextOverflow) || calls != 2 || compactions != 1 {
				t.Fatal(err, calls, compactions)
			}
		})
	}
}

func TestApprovedCompactionCancellationAfterCommitPreventsDispatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := runRequest()
	r.ParentTaskID = "parent"
	r.MaxContextTokens = 4000
	r.ApprovedCompaction = approvedMidTaskCompaction(r)
	events := []runtime.Event{}
	providerCalls := 0
	loop := runtime.Loop{
		ContextEstimator: loopContextEstimator(growingContextEstimator),
		Journal: journal(func(_ context.Context, _ int64, event runtime.Event) error {
			events = append(events, event)
			if event.Kind == runtime.ContextCompacted {
				cancel()
			}
			return nil
		}),
		Provider: model(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
			providerCalls++
			return emitCall(emit)
		}),
		Tools: executor(func(context.Context, providers.ToolCall) (runtime.ToolResult, error) {
			return runtime.ToolResult{Content: "evidence", Effect: runtime.NoEffect}, nil
		}),
	}
	_, err := loop.Run(ctx, r)
	if !errors.Is(err, context.Canceled) || providerCalls != 1 {
		t.Fatal(err, providerCalls)
	}
	compactionIndex := -1
	for i, event := range events {
		if event.Kind == runtime.ContextCompacted {
			compactionIndex = i
		}
		if compactionIndex >= 0 && i > compactionIndex && event.Kind == runtime.TurnStarted {
			t.Fatal("dispatch followed cancellation at activation boundary", events)
		}
	}
	if compactionIndex < 0 || events[len(events)-1].Kind != runtime.TaskCanceled {
		t.Fatal("durable cancellation sequence incomplete", events)
	}
}

func TestApprovedCompactionContractFailsBeforePersistence(t *testing.T) {
	for _, mode := range []string{"original_mismatch", "missing_review", "already_compacted"} {
		t.Run(mode, func(t *testing.T) {
			r := runRequest()
			r.ParentTaskID = "parent"
			r.MaxContextTokens = 4000
			r.ApprovedCompaction = approvedMidTaskCompaction(r)
			switch mode {
			case "original_mismatch":
				r.ApprovedCompaction.OriginalPrefix[0].Content = "different"
			case "missing_review":
				r.ApprovedCompaction.Compaction.SummaryReviewID = ""
			case "already_compacted":
				r.Compaction = compactionFixture()
			}
			loop := runtime.Loop{
				Journal: journal(func(context.Context, int64, runtime.Event) error {
					t.Fatal("invalid prepared compaction persisted")
					return nil
				}),
				Provider: model(func(context.Context, providers.Request, func(providers.Chunk) error) error {
					t.Fatal("invalid prepared compaction dispatched")
					return nil
				}),
			}
			if _, err := loop.Run(context.Background(), r); !errors.Is(err, runtime.ErrInvalidRun) {
				t.Fatal(err)
			}
		})
	}
}
