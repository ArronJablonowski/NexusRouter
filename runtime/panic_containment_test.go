package runtime_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func TestProviderPanicIsSanitizedAndDurablyTerminal(t *testing.T) {
	for _, afterDelta := range []bool{false, true} {
		name := "before_output"
		if afterDelta {
			name = "after_committed_delta"
		}
		t.Run(name, func(t *testing.T) {
			db, _ := store(t)
			calls := 0
			loop := runtime.Loop{Journal: db, Provider: model(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
				calls++
				if afterDelta {
					if err := emit(providers.Chunk{Text: "committed partial"}); err != nil {
						return err
					}
				}
				panic("private provider panic")
			})}
			result, err := loop.Run(context.Background(), runRequest())
			if !errors.Is(err, runtime.ErrProvider) || result.Retryable || calls != 1 || strings.Contains(err.Error(), "private") {
				t.Fatal(result, err, calls)
			}
			var failure *providers.Failure
			if !errors.As(err, &failure) || failure.Code != "adapter_failure" || failure.Retryable || failure.Partial != afterDelta {
				t.Fatal("incorrect normalized panic", failure, err)
			}
			events, readErr := db.Read(context.Background(), "task", 0, 100)
			if readErr != nil || events[len(events)-1].Kind != runtime.TaskFailed || events[len(events)-1].Data.Code != "execution_failed" {
				t.Fatal(events, readErr)
			}
			deltas, terminals := 0, 0
			for _, event := range events {
				if event.Kind == runtime.ModelDelta {
					deltas++
				}
				if event.Kind == runtime.TaskFailed || event.Kind == runtime.TaskCanceled || event.Kind == runtime.TaskCompleted {
					terminals++
				}
			}
			if (deltas == 1) != afterDelta || terminals != 1 {
				t.Fatal("committed delta or terminal mismatch", deltas, terminals)
			}
		})
	}
}

func TestProviderPanicPreservesCallbackBoundaryError(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		kind runtime.Kind
		code string
	}{
		{name: "persistence", err: errors.New("private persistence failure"), kind: runtime.ModelDelta},
		{name: "cancellation", err: runtime.ErrCancellationRequested, kind: runtime.TaskCanceled, code: "canceled"},
		{name: "lease_loss", err: runtime.ErrExecutionLeaseLost, kind: runtime.TaskCanceled, code: "execution_lease_lost"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, _ := store(t)
			injected := false
			loop := runtime.Loop{Journal: journal(func(ctx context.Context, sequence int64, event runtime.Event) error {
				if event.Kind == runtime.ModelDelta && !injected {
					injected = true
					if tc.name == "persistence" {
						return tc.err
					}
					return tc.err
				}
				return db.Append(ctx, sequence, event)
			}), Provider: model(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
				_ = emit(providers.Chunk{Text: "not acknowledged"})
				panic("private provider panic")
			})}
			_, err := loop.Run(context.Background(), runRequest())
			if !injected || strings.Contains(err.Error(), "private") {
				t.Fatal(injected, err)
			}
			if tc.name == "persistence" {
				if !errors.Is(err, runtime.ErrPersistence) || errors.Is(err, runtime.ErrProvider) {
					t.Fatal("persistence ambiguity lost", err)
				}
			} else if !errors.Is(err, tc.err) || !errors.Is(err, context.Canceled) || errors.Is(err, runtime.ErrProvider) {
				t.Fatal("callback control error lost", err)
			}
			events, readErr := db.Read(context.Background(), "task", 0, 100)
			if readErr != nil {
				t.Fatal(readErr)
			}
			terminals := 0
			for _, event := range events {
				if event.Kind == runtime.TaskFailed || event.Kind == runtime.TaskCanceled || event.Kind == runtime.TaskCompleted {
					terminals++
				}
			}
			if tc.name == "persistence" {
				if events[len(events)-1].Kind != runtime.TurnStarted {
					t.Fatal("ambiguous append gained terminal", events)
				}
				if terminals != 0 {
					t.Fatal("ambiguous append gained terminal", terminals)
				}
			} else if events[len(events)-1].Kind != tc.kind || events[len(events)-1].Data.Code != tc.code {
				t.Fatal("missing control terminal", events)
			} else if terminals != 1 {
				t.Fatal("control terminal duplicated", terminals)
			}
		})
	}
}

func TestProviderCallbackPanicBecomesPersistenceAmbiguity(t *testing.T) {
	for _, providerPanics := range []bool{false, true} {
		t.Run(map[bool]string{false: "provider_ignores_callback", true: "provider_panics"}[providerPanics], func(t *testing.T) {
			db, _ := store(t)
			loop := runtime.Loop{Journal: journal(func(ctx context.Context, sequence int64, event runtime.Event) error {
				if event.Kind == runtime.ModelDelta {
					panic("private journal panic")
				}
				return db.Append(ctx, sequence, event)
			}), Provider: model(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
				_ = emit(providers.Chunk{Text: "commit unknown"})
				if providerPanics {
					panic("private provider panic")
				}
				return nil
			})}
			_, err := loop.Run(context.Background(), runRequest())
			if !errors.Is(err, runtime.ErrPersistence) || errors.Is(err, runtime.ErrProvider) || strings.Contains(err.Error(), "private") {
				t.Fatal(err)
			}
			events, readErr := db.Read(context.Background(), "task", 0, 100)
			if readErr != nil || events[len(events)-1].Kind != runtime.TurnStarted {
				t.Fatal(events, readErr)
			}
		})
	}
}

func TestProviderCallbackCommitThenPanicDoesNotInventTerminal(t *testing.T) {
	db, _ := store(t)
	loop := runtime.Loop{Journal: journal(func(ctx context.Context, sequence int64, event runtime.Event) error {
		if err := db.Append(ctx, sequence, event); err != nil {
			return err
		}
		if event.Kind == runtime.ModelDelta {
			panic("private post-commit panic")
		}
		return nil
	}), Provider: model(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
		_ = emit(providers.Chunk{Text: "committed but unacknowledged"})
		return nil
	})}
	_, err := loop.Run(context.Background(), runRequest())
	if !errors.Is(err, runtime.ErrPersistence) || errors.Is(err, runtime.ErrProvider) || strings.Contains(err.Error(), "private") {
		t.Fatal(err)
	}
	events, readErr := db.Read(context.Background(), "task", 0, 100)
	if readErr != nil || events[len(events)-1].Kind != runtime.ModelDelta || terminalCountForRuntime(events) != 0 {
		t.Fatal(events, readErr)
	}
}

func TestProviderCallbackErrorIsLatchedAgainstRepeatedEmit(t *testing.T) {
	db, _ := store(t)
	appendAttempts := 0
	loop := runtime.Loop{Journal: journal(func(ctx context.Context, sequence int64, event runtime.Event) error {
		appendAttempts++
		if event.Kind == runtime.ModelDelta {
			return errors.New("private persistence failure")
		}
		return db.Append(ctx, sequence, event)
	}), Provider: model(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
		first := emit(providers.Chunk{Text: "uncommitted"})
		second := emit(providers.Chunk{Done: true, FinishReason: "stop"})
		if !errors.Is(first, runtime.ErrPersistence) || !errors.Is(second, runtime.ErrPersistence) {
			t.Fatalf("provider callback did not retain its first error: %v %v", first, second)
		}
		return nil
	})}
	_, err := loop.Run(context.Background(), runRequest())
	if !errors.Is(err, runtime.ErrPersistence) || errors.Is(err, runtime.ErrProvider) || appendAttempts != 3 || strings.Contains(err.Error(), "private") {
		t.Fatal(err, appendAttempts)
	}
	events, readErr := db.Read(context.Background(), "task", 0, 100)
	if readErr != nil || events[len(events)-1].Kind != runtime.TurnStarted {
		t.Fatal(events, readErr)
	}
}

func terminalCountForRuntime(events []runtime.Event) int {
	count := 0
	for _, event := range events {
		if event.Kind == runtime.TaskCompleted || event.Kind == runtime.TaskFailed || event.Kind == runtime.TaskCanceled {
			count++
		}
	}
	return count
}

func TestProviderPanicAfterUncommittedControlOutputDoesNotDispatch(t *testing.T) {
	for _, tc := range []struct {
		name  string
		chunk providers.Chunk
	}{
		{name: "tool_proposal", chunk: providers.Chunk{ToolCall: &providers.ToolCall{ID: "call1", Name: "lookup", Arguments: []byte(`{}`)}}},
		{name: "done", chunk: providers.Chunk{Done: true, FinishReason: "stop"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, _ := store(t)
			toolCalls := 0
			loop := runtime.Loop{Journal: db, Provider: model(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
				if err := emit(tc.chunk); err != nil {
					return err
				}
				panic("private provider panic")
			}), Tools: executor(func(context.Context, providers.ToolCall) (runtime.ToolResult, error) {
				toolCalls++
				return runtime.ToolResult{}, nil
			})}
			_, err := loop.Run(context.Background(), runRequest())
			var failure *providers.Failure
			if !errors.Is(err, runtime.ErrProvider) || !errors.As(err, &failure) || failure.Partial || failure.Retryable || toolCalls != 0 {
				t.Fatal(err, failure, toolCalls)
			}
			events, readErr := db.Read(context.Background(), "task", 0, 100)
			if readErr != nil || events[len(events)-1].Kind != runtime.TaskFailed {
				t.Fatal(events, readErr)
			}
			for _, event := range events {
				if event.Kind == runtime.TurnCompleted || event.Kind == runtime.ToolStarted {
					t.Fatal("panic released incomplete output", event)
				}
			}
		})
	}
}

type panickingScopedExecutor struct{ calls *int }

func (p panickingScopedExecutor) Execute(context.Context, providers.ToolCall) (runtime.ToolResult, error) {
	panic("legacy path must not be selected")
}

func (p panickingScopedExecutor) ExecuteScoped(context.Context, runtime.ToolExecution) (runtime.ToolResult, error) {
	*p.calls++
	panic("private scoped tool panic")
}

func TestToolExecutorPanicPersistsUncertainFailureWithoutRetry(t *testing.T) {
	for _, scoped := range []bool{false, true} {
		name := "legacy"
		if scoped {
			name = "scoped"
		}
		t.Run(name, func(t *testing.T) {
			db, _ := store(t)
			providerCalls, toolCalls := 0, 0
			loop := runtime.Loop{Journal: db, Provider: model(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
				providerCalls++
				return emitCall(emit)
			})}
			if scoped {
				loop.Tools = panickingScopedExecutor{calls: &toolCalls}
			} else {
				loop.Tools = executor(func(context.Context, providers.ToolCall) (runtime.ToolResult, error) {
					toolCalls++
					panic("private legacy tool panic")
				})
			}
			_, err := loop.Run(context.Background(), runRequest())
			if !errors.Is(err, runtime.ErrTool) || providerCalls != 1 || toolCalls != 1 || strings.Contains(err.Error(), "private") {
				t.Fatal(err, providerCalls, toolCalls)
			}
			events, readErr := db.Read(context.Background(), "task", 0, 100)
			if readErr != nil || len(events) < 2 {
				t.Fatal(events, readErr)
			}
			completedCount, terminalCount := 0, 0
			for _, event := range events {
				if event.Kind == runtime.ToolCompleted {
					completedCount++
				}
				if event.Kind == runtime.TaskFailed || event.Kind == runtime.TaskCanceled || event.Kind == runtime.TaskCompleted {
					terminalCount++
				}
			}
			completed, terminal := events[len(events)-2], events[len(events)-1]
			if completedCount != 1 || terminalCount != 1 || completed.Kind != runtime.ToolCompleted || completed.Data.Code != "tool_failed" || completed.Data.Effect != runtime.UncertainEffect || completed.Data.Text != "" || terminal.Kind != runtime.TaskFailed || terminal.Data.Code != "execution_failed" {
				t.Fatal("unsafe panic disposition", completed, terminal)
			}
			toolCompletions, terminals := 0, 0
			for _, event := range events {
				if event.Kind == runtime.ToolCompleted {
					toolCompletions++
				}
				if event.Kind == runtime.TaskFailed || event.Kind == runtime.TaskCanceled || event.Kind == runtime.TaskCompleted {
					terminals++
				}
			}
			if toolCompletions != 1 || terminals != 1 {
				t.Fatal("panic cleanup duplicated", toolCompletions, terminals)
			}
		})
	}
}

func TestToolPanicThenCancellationPreservesUncertainEffect(t *testing.T) {
	db, _ := store(t)
	ctx, cancel := context.WithCancel(context.Background())
	toolCalls := 0
	loop := runtime.Loop{Journal: db, Provider: model(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
		return emitCall(emit)
	}), Tools: executor(func(context.Context, providers.ToolCall) (runtime.ToolResult, error) {
		toolCalls++
		cancel()
		panic("private tool panic")
	})}
	_, err := loop.Run(ctx, runRequest())
	if !errors.Is(err, context.Canceled) || errors.Is(err, runtime.ErrPersistence) || toolCalls != 1 || strings.Contains(err.Error(), "private") {
		t.Fatal(err, toolCalls)
	}
	events, readErr := db.Read(context.Background(), "task", 0, 100)
	if readErr != nil || len(events) < 2 {
		t.Fatal(events, readErr)
	}
	completed, terminal := events[len(events)-2], events[len(events)-1]
	if completed.Kind != runtime.ToolCompleted || completed.Data.Effect != runtime.UncertainEffect || completed.Data.Code != "tool_failed" || terminal.Kind != runtime.TaskCanceled || terminal.Data.Code != "canceled" {
		t.Fatal(completed, terminal)
	}
}

func TestToolPanicCompletionPersistenceFailureDoesNotInventTerminal(t *testing.T) {
	db, _ := store(t)
	loop := runtime.Loop{Journal: journal(func(ctx context.Context, sequence int64, event runtime.Event) error {
		if event.Kind == runtime.ToolCompleted {
			return errors.New("private persistence failure")
		}
		return db.Append(ctx, sequence, event)
	}), Provider: model(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
		return emitCall(emit)
	}), Tools: executor(func(context.Context, providers.ToolCall) (runtime.ToolResult, error) {
		panic("private tool panic")
	})}
	_, err := loop.Run(context.Background(), runRequest())
	if !errors.Is(err, runtime.ErrPersistence) || errors.Is(err, runtime.ErrTool) || strings.Contains(err.Error(), "private") {
		t.Fatal(err)
	}
	events, readErr := db.Read(context.Background(), "task", 0, 100)
	if readErr != nil || events[len(events)-1].Kind != runtime.ToolStarted {
		t.Fatal(events, readErr)
	}
}
