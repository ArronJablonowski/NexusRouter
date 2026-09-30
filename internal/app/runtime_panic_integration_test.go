package app

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

const applicationProviderPanic = "private-application-provider-panic"

type partialPanicProvider struct{}

func (partialPanicProvider) Models(context.Context) ([]string, error) {
	return []string{"fixture"}, nil
}

func (partialPanicProvider) Stream(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
	if err := emit(providers.Chunk{Text: "visible partial fixture-"}); err != nil {
		return err
	}
	panic(applicationProviderPanic)
}

type partialPanicFactory struct{}

func (partialPanicFactory) Build(context.Context, providers.Connection) (providers.Provider, error) {
	return partialPanicProvider{}, nil
}

func panicStreamService(t *testing.T) (*Service, string) {
	t.Helper()
	s, path := streamService(t, func(http.ResponseWriter, *http.Request) {
		t.Error("custom panic provider unexpectedly reached HTTP transport")
	})
	s.providerFactory = partialPanicFactory{}
	return s, path
}

func terminalCount(events []runtime.Event) int {
	count := 0
	for _, event := range events {
		if event.Kind == runtime.TaskFailed || event.Kind == runtime.TaskCanceled || event.Kind == runtime.TaskCompleted {
			count++
		}
	}
	return count
}

func TestProviderPanicAfterCommittedDeltaReachesConfiguredAndPerCallEventSinks(t *testing.T) {
	s, path := panicStreamService(t)
	var configured, perCall []runtime.Event
	if err := installEventSink(s, runtime.EventSinkFunc(func(_ context.Context, event runtime.Event) error {
		configured = append(configured, event)
		return nil
	})); err != nil {
		t.Fatal(err)
	}
	result, err := s.RunStream(context.Background(), Request{ModelID: "chat", Prompt: "panic after output"}, func(event runtime.Event) error {
		perCall = append(perCall, event)
		return nil
	})
	if !errors.Is(err, runtime.ErrProvider) || result.TaskID == "" || result.retryable || strings.Contains(err.Error(), applicationProviderPanic) {
		t.Fatal("provider panic was not sanitized", result, err)
	}
	store, openErr := telemetry.OpenReadOnly(context.Background(), path)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer store.Close()
	durable, readErr := store.Read(context.Background(), result.TaskID, 0, 100)
	if readErr != nil {
		t.Fatal(readErr)
	}
	for name, events := range map[string][]runtime.Event{"durable": durable, "configured": configured, "per_call": perCall} {
		if len(events) < 4 || events[len(events)-1].Kind != runtime.TaskFailed || events[len(events)-1].Data.Code != "execution_failed" || terminalCount(events) != 1 {
			t.Fatalf("%s did not observe exactly one failed terminal: %+v", name, events)
		}
		deltas := 0
		for _, event := range events {
			body, encodeErr := event.Encode()
			if encodeErr != nil || strings.Contains(string(body), applicationProviderPanic) {
				t.Fatalf("%s observed unsanitized event: %s %v", name, body, encodeErr)
			}
			if event.Kind == runtime.ModelDelta {
				deltas++
				if event.Data.Text != "" {
					t.Fatalf("%s received persisted provisional text", name)
				}
			}
		}
		if deltas != 1 {
			t.Fatalf("%s observed %d committed deltas", name, deltas)
		}
	}
	if len(configured) != len(durable) || len(perCall) != len(durable) {
		t.Fatal("event sinks missed or duplicated committed events", len(durable), len(configured), len(perCall))
	}
	for i := range durable {
		want, _ := durable[i].Encode()
		gotConfigured, _ := configured[i].Encode()
		gotPerCall, _ := perCall[i].Encode()
		if string(want) != string(gotConfigured) || string(want) != string(gotPerCall) {
			t.Fatal("event sink projection differs from durable order", i)
		}
	}
}

func TestProviderPanicDoesNotFlushFailedTextStreamTail(t *testing.T) {
	s, path := panicStreamService(t)
	var chunks []string
	result, err := s.RunTextStream(context.Background(), Request{ModelID: "chat", Prompt: "panic after output"}, func(text string) error {
		chunks = append(chunks, text)
		return nil
	})
	if !errors.Is(err, runtime.ErrProvider) || result.TaskID == "" || result.retryable || strings.Contains(err.Error(), applicationProviderPanic) {
		t.Fatal("provider panic was not sanitized", result, err)
	}
	// The unmatched "fixture-" suffix is a possible prefix of the configured
	// secret. It must be discarded with the failed task, never final-flushed.
	if got := strings.Join(chunks, ""); got != "visible partial " || strings.Contains(got, "fixture-") || strings.Contains(got, "[REDACTED]") {
		t.Fatal("failed text stream flushed provisional tail", chunks)
	}
	store, openErr := telemetry.OpenReadOnly(context.Background(), path)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer store.Close()
	events, readErr := store.Read(context.Background(), result.TaskID, 0, 100)
	if readErr != nil || len(events) < 4 || events[len(events)-1].Kind != runtime.TaskFailed || terminalCount(events) != 1 {
		t.Fatal("failed text stream missing unique durable terminal", events, readErr)
	}
}
