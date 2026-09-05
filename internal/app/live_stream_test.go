package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func TestLiveStreamOrderDurabilityAndRedaction(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	release := make(chan struct{})
	prefix := strings.Repeat("safe ", 50)
	s, path := streamService(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":false}\n", prefix+"fixture-")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		fmt.Fprintln(w, `{"message":{"content":"secret end"},"done":true,"done_reason":"stop"}`)
	})
	var last runtime.Event
	var text strings.Builder
	result, err := s.RunLiveStream(ctx, Request{ModelID: "chat", Prompt: "hello"}, func(e runtime.Event) error {
		last = e
		if e.Kind == runtime.ModelDelta && e.Data.Text != "" {
			t.Fatal("raw text in event")
		}
		return nil
	}, func(chunk string) error {
		if last.Kind != runtime.ModelDelta && last.Kind != runtime.TaskCompleted {
			t.Fatal("text before matching lifecycle", last.Kind)
		}
		db, e := telemetry.OpenReadOnly(ctx, path)
		if e != nil {
			return e
		}
		defer db.Close()
		events, e := db.Read(ctx, last.TaskID, last.Sequence-1, 1)
		if e != nil {
			return e
		}
		if len(events) != 1 || events[0].ID != last.ID {
			t.Fatal("text before commit")
		}
		if text.Len() == 0 {
			close(release)
		}
		text.WriteString(chunk)
		return nil
	})
	if err != nil || text.String() != prefix+"[REDACTED] end" || result.Text != text.String() {
		t.Fatal(result, text.String(), err)
	}
}

func TestLiveStreamSharedFailureGate(t *testing.T) {
	for _, channel := range []string{"event", "text"} {
		for _, panics := range []bool{false, true} {
			t.Run(fmt.Sprint(channel, panics), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				stopped := make(chan struct{})
				s, _ := streamService(t, func(w http.ResponseWriter, r *http.Request) {
					defer close(stopped)
					fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":false}\n", strings.Repeat("safe ", 100))
					w.(http.Flusher).Flush()
					select {
					case <-r.Context().Done():
					case <-ctx.Done():
					}
				})
				failed := false
				fail := func() error {
					failed = true
					if panics {
						panic("private")
					}
					return errors.New("private")
				}
				_, err := s.RunLiveStream(ctx, Request{ModelID: "chat", Prompt: "hello"}, func(e runtime.Event) error {
					if failed {
						t.Fatal("event after failure")
					}
					if channel == "event" && e.Kind == runtime.ModelDelta {
						return fail()
					}
					return nil
				}, func(string) error {
					if failed {
						t.Fatal("text after failure")
					}
					if channel == "text" {
						return fail()
					}
					t.Fatal("text delivered after rejected delta event")
					return nil
				})
				if !failed || !errors.Is(err, ErrEventDelivery) || strings.Contains(err.Error(), "private") {
					t.Fatal(err)
				}
				select {
				case <-stopped:
				case <-ctx.Done():
					t.Fatal("provider not joined")
				}
			})
		}
	}
}

func TestLiveTextIgnoresToolAndWorkerPayloads(t *testing.T) {
	var text strings.Builder
	d := textDelivery{emit: func(s string) { text.WriteString(s) }}
	d.accept(runtime.TurnStarted, "")
	for _, kind := range []runtime.Kind{runtime.ToolStarted, runtime.ToolCompleted, runtime.WorkerStarted, runtime.WorkerCompleted} {
		d.accept(kind, "private tool or child payload")
	}
	d.accept(runtime.TaskCompleted, "")
	if text.Len() != 0 {
		t.Fatal("non-model content leaked", text.String())
	}
}

func TestLiveStreamTerminalCancellationRetainsCommit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, path := streamService(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `{"message":{"content":"fixture-"},"done":true,"done_reason":"stop"}`)
	})
	var terminal runtime.Event
	_, err := s.RunLiveStream(ctx, Request{ModelID: "chat", Prompt: "hello"}, func(e runtime.Event) error {
		if e.Kind == runtime.TaskCompleted {
			terminal = e
			cancel()
		}
		return nil
	}, func(string) error { t.Fatal("buffered text delivered after cancellation"); return nil })
	if !errors.Is(err, context.Canceled) || terminal.ID == "" {
		t.Fatal(terminal, err)
	}
	db, err := telemetry.OpenReadOnly(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	events, err := db.Read(context.Background(), terminal.TaskID, terminal.Sequence-1, 1)
	if err != nil || len(events) != 1 || events[0].Kind != runtime.TaskCompleted {
		t.Fatal(events, err)
	}
}
