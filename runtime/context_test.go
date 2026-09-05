package runtime_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func TestContextGrowthStopsBeforeNextProviderDispatch(t *testing.T) {
	s, _ := store(t)
	dispatches := 0
	l := runtime.Loop{Journal: s, Provider: model(func(_ context.Context, _ providers.Request, e func(providers.Chunk) error) error {
		dispatches++
		return emitCall(e)
	}), Tools: executor(func(context.Context, providers.ToolCall) (runtime.ToolResult, error) {
		return runtime.ToolResult{Content: strings.Repeat("x", 4096), Effect: runtime.NoEffect}, nil
	})}
	r := runRequest()
	r.MaxOutputBytes = 8192
	r.MaxContextTokens = 2048
	_, err := l.Run(context.Background(), r)
	if !errors.Is(err, runtime.ErrLimit) || dispatches != 1 {
		t.Fatalf("dispatches=%d err=%v", dispatches, err)
	}
	events, err := s.Read(context.Background(), r.TaskID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	last := events[len(events)-1]
	if last.Kind != runtime.TaskFailed || last.Data.Code != "budget_exhausted" {
		t.Fatalf("%+v", last)
	}
	completed := false
	for _, e := range events {
		completed = completed || e.Kind == runtime.ToolCompleted
	}
	if !completed {
		t.Fatal("tool result was lost")
	}
}

func TestInitialContextLimitPreventsDispatch(t *testing.T) {
	s, _ := store(t)
	l := runtime.Loop{Journal: s, Provider: model(func(context.Context, providers.Request, func(providers.Chunk) error) error {
		t.Fatal("oversized context dispatched")
		return nil
	})}
	r := runRequest()
	r.MaxContextTokens = 1
	if _, err := l.Run(context.Background(), r); !errors.Is(err, runtime.ErrLimit) {
		t.Fatal(err)
	}
}
