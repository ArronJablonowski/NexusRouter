package runtime_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func TestAllocatedContextBoundsInitialDispatch(t *testing.T) {
	for _, maximum := range []int{0, 8192} {
		request := runRequest()
		request.MaxContextTokens = maximum
		request.Inference.ContextTokens = 2048
		request.Inference.Messages[0].Content = strings.Repeat("x", 4096)
		events := []runtime.Event{}
		calls := 0
		loop := runtime.Loop{
			Journal: journal(func(_ context.Context, _ int64, event runtime.Event) error {
				events = append(events, event)
				return nil
			}),
			Provider: model(func(context.Context, providers.Request, func(providers.Chunk) error) error { calls++; return nil }),
		}
		_, err := loop.Run(context.Background(), request)
		assertContextFailure(t, events, err, runtime.ErrContextOverflow, "context_overflow", 0)
		if calls != 0 {
			t.Fatal("oversized input reached allocated provider", calls)
		}
	}
}

func TestAllocatedContextBoundsGrowingToolHistory(t *testing.T) {
	request := runRequest()
	request.MaxContextTokens = 16384
	request.Inference.ContextTokens = 2048
	request.MaxOutputBytes = 8192
	journal := &compactionJournal{}
	calls := 0
	loop := runtime.Loop{
		Journal: journal,
		Provider: model(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
			calls++
			if calls == 1 {
				return emitCall(emit)
			}
			return emit(providers.Chunk{Text: "overflow was dispatched", Done: true, FinishReason: "stop"})
		}),
		Tools: executor(func(context.Context, providers.ToolCall) (runtime.ToolResult, error) {
			return runtime.ToolResult{Content: strings.Repeat("x", 4096), Effect: runtime.NoEffect}, nil
		}),
	}
	_, err := loop.Run(context.Background(), request)
	assertContextFailure(t, journal.events, err, runtime.ErrContextOverflow, "context_overflow", 1)
	if calls != 1 {
		t.Fatal("growing history exceeded allocation", calls)
	}
	retained := false
	for _, event := range journal.events {
		retained = retained || event.Kind == runtime.ToolCompleted
	}
	if !retained {
		t.Fatal("tool result was lost")
	}
}

func TestAllocatedContextTriggersApprovedCompaction(t *testing.T) {
	request := runRequest()
	request.ParentTaskID = "parent"
	request.MaxContextTokens = 16384
	request.Inference.ContextTokens = 4000
	request.ApprovedCompaction = approvedMidTaskCompaction(request)
	journal := &compactionJournal{}
	calls := 0
	loop := runtime.Loop{
		Journal:          journal,
		ContextEstimator: loopContextEstimator(growingContextEstimator),
		Provider: model(func(_ context.Context, in providers.Request, emit func(providers.Chunk) error) error {
			calls++
			if in.ContextTokens != 4000 {
				t.Fatal("allocation changed without admission")
			}
			if calls == 1 {
				return emitCall(emit)
			}
			if in.Messages[0].Content != "approved summary" {
				t.Error("allocated window did not trigger compaction")
			}
			return emit(providers.Chunk{Text: "done", Done: true, FinishReason: "stop"})
		}),
		Tools: executor(func(context.Context, providers.ToolCall) (runtime.ToolResult, error) {
			return runtime.ToolResult{Content: "evidence", Effect: runtime.NoEffect}, nil
		}),
	}
	result, err := loop.Run(context.Background(), request)
	if err != nil || result.Text != "done" || calls != 2 {
		t.Fatal(result, err, calls)
	}
	compactions := 0
	for _, event := range journal.events {
		if event.Kind == runtime.ContextCompacted {
			compactions++
		}
	}
	if compactions != 1 {
		t.Fatal("expected one durable compaction", compactions)
	}
}

func TestAllocatedContextRejectsInvalidProviderLimits(t *testing.T) {
	for _, allocation := range []int64{-1, 8193, providers.MaxOutputTokens + 1} {
		request := runRequest()
		request.MaxContextTokens = 8192
		request.Inference.ContextTokens = allocation
		journal := &compactionJournal{}
		loop := runtime.Loop{Journal: journal, Provider: model(func(context.Context, providers.Request, func(providers.Chunk) error) error {
			t.Fatal("invalid allocation dispatched")
			return nil
		})}
		if _, err := loop.Run(context.Background(), request); !errors.Is(err, runtime.ErrInvalidRun) || len(journal.events) != 0 {
			t.Fatal("invalid allocation created durable task", allocation, err)
		}
	}
}
