package runtime_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func TestRetryabilityRequiresNoOutputAndExplicitProviderEvidence(t *testing.T) {
	for _, tc := range []struct{ partial, text, tool, retry, want bool }{{false, false, false, true, true}, {true, false, false, true, false}, {false, true, false, true, false}, {false, false, true, true, false}, {false, false, false, false, false}} {
		s, _ := store(t)
		l := runtime.Loop{Journal: s, Provider: model(func(_ context.Context, _ providers.Request, e func(providers.Chunk) error) error {
			if tc.text {
				e(providers.Chunk{Text: "partial"})
			}
			if tc.tool {
				e(providers.Chunk{ToolCall: &providers.ToolCall{ID: "call", Name: "tool", Arguments: []byte(`{}`)}})
			}
			return &providers.Failure{Code: "transport", Retryable: tc.retry, Partial: tc.partial}
		})}
		out, err := l.Run(context.Background(), runRequest())
		if err == nil || out.Retryable != tc.want {
			t.Fatalf("%+v %+v %v", tc, out, err)
		}
	}
}

func TestFailedTerminalPersistenceCannotAuthorizeRetry(t *testing.T) {
	l := runtime.Loop{Journal: journal(func(_ context.Context, _ int64, e runtime.Event) error {
		if e.Kind == runtime.TaskFailed {
			return errors.New("storage failed")
		}
		return nil
	}), Provider: model(func(context.Context, providers.Request, func(providers.Chunk) error) error {
		return &providers.Failure{Code: "transport", Retryable: true}
	})}
	out, err := l.Run(context.Background(), runRequest())
	if !errors.Is(err, runtime.ErrPersistence) || out.Retryable {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestContextOverflowIsDurableAndNeverRetryable(t *testing.T) {
	s, _ := store(t)
	l := runtime.Loop{Journal: s, Provider: model(func(context.Context, providers.Request, func(providers.Chunk) error) error {
		// Even a malformed custom provider cannot turn a context failure into
		// retry authority by setting Retryable itself.
		return &providers.Failure{Code: "context_overflow", Retryable: true}
	})}
	out, err := l.Run(context.Background(), runRequest())
	if err == nil || out.Retryable {
		t.Fatalf("context overflow authorized retry: %+v %v", out, err)
	}
	events, readErr := s.Read(context.Background(), "task", 0, 100)
	if readErr != nil || len(events) == 0 || events[len(events)-1].Kind != runtime.TaskFailed || events[len(events)-1].Data.Code != "context_overflow" {
		t.Fatalf("context overflow not durable: %v %v", events, readErr)
	}
}

func TestStreamRecoveryRequiresFirstTurnWithoutToolsAndDurableFailure(t *testing.T) {
	for _, mode := range []string{"text", "tool", "persist", "canceled", "unknown", "non-stop-finish"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := store(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			l := runtime.Loop{Journal: s, Provider: model(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
				calls++
				if mode == "non-stop-finish" && calls == 1 {
					return emit(providers.Chunk{Text: "continue", Done: true, FinishReason: "length"})
				}
				if mode == "tool" {
					if err := emit(providers.Chunk{ToolCall: &providers.ToolCall{ID: "call", Name: "tool", Arguments: []byte(`{}`)}}); err != nil {
						return err
					}
				} else {
					if err := emit(providers.Chunk{Text: "incomplete"}); err != nil {
						return err
					}
				}
				if mode == "canceled" {
					cancel()
				}
				code := "invalid_stream"
				if mode == "unknown" {
					code = "adapter_failure"
				}
				return &providers.Failure{Code: code, Partial: true}
			})}
			if mode == "persist" {
				l.Journal = journal(func(_ context.Context, _ int64, e runtime.Event) error {
					if e.Kind == runtime.TaskFailed {
						return errors.New("unknown commit")
					}
					return nil
				})
			}
			out, err := l.Run(ctx, runRequest())
			if err == nil || out.Retryable != (mode == "text") {
				t.Fatal(mode, out, err)
			}
		})
	}
}
