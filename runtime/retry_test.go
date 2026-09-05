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
