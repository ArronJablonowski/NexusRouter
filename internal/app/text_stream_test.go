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

func TestTextStreamLiveRedactionAndDurability(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	live := make(chan struct{})
	prefix := strings.Repeat("safe text ", 30)
	s, path := streamService(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":false}\n", prefix+"fixture-")
		w.(http.Flusher).Flush()
		select {
		case <-live:
		case <-r.Context().Done():
			return
		case <-ctx.Done():
			return
		}
		fmt.Fprintln(w, `{"message":{"content":"secret answer"},"done":true,"done_reason":"stop"}`)
	})
	var text strings.Builder
	var taskID string
	r := Request{ModelID: "chat", Prompt: "hello"}
	r.eventSink = func(e runtime.Event) { taskID = e.TaskID }
	result, err := s.RunTextStream(ctx, r, func(chunk string) error {
		db, err := telemetry.OpenReadOnly(ctx, path)
		if err != nil {
			return err
		}
		defer db.Close()
		events, err := db.Read(ctx, taskID, 0, 100)
		if err != nil {
			return err
		}
		found := false
		for _, e := range events {
			if e.Kind == runtime.ModelDelta {
				found = true
				if e.Data.Text != "" {
					t.Error("delta text persisted")
				}
			}
		}
		if !found {
			t.Error("text delivered before delta commit")
		}
		if text.Len() == 0 {
			close(live)
		}
		text.WriteString(chunk)
		return nil
	})
	if err != nil || text.String() != prefix+"[REDACTED] answer" || result.Text != text.String() {
		t.Fatal(result, text.String(), err)
	}
}

func TestTextStreamSinkFailureCancels(t *testing.T) {
	for _, panicSink := range []bool{false, true} {
		t.Run(fmt.Sprint(panicSink), func(t *testing.T) {
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
			calls := 0
			_, err := s.RunTextStream(ctx, Request{ModelID: "chat", Prompt: "hello"}, func(string) error {
				calls++
				if panicSink {
					panic("private sink failure")
				}
				return errors.New("private sink failure")
			})
			if !errors.Is(err, ErrEventDelivery) || calls != 1 || strings.Contains(err.Error(), "private") {
				t.Fatal(calls, err)
			}
			select {
			case <-stopped:
			case <-ctx.Done():
				t.Fatal("provider not canceled")
			}
		})
	}
}

func TestTextDeliveryUTF8AndFailedTail(t *testing.T) {
	var got strings.Builder
	d := &textDelivery{emit: func(s string, _ bool) { got.WriteString(s) }}
	d.accept(runtime.TurnStarted, "")
	d.accept(runtime.ModelDelta, "\xe2")
	if got.Len() != 0 {
		t.Fatal("partial rune emitted")
	}
	d.accept(runtime.ModelDelta, "\x82\xac")
	d.accept(runtime.TurnCompleted, "")
	d.accept(runtime.TaskCompleted, "")
	if got.String() != "€" {
		t.Fatal(got.String())
	}
	got.Reset()
	d = &textDelivery{secrets: []string{"secret"}, emit: func(s string, _ bool) { got.WriteString(s) }}
	d.accept(runtime.TurnStarted, "")
	d.accept(runtime.ModelDelta, "secr")
	d.accept(runtime.TaskFailed, "")
	d.accept(runtime.TurnCompleted, "")
	d.accept(runtime.TaskCompleted, "")
	if got.Len() != 0 {
		t.Fatal("failed partial secret flushed")
	}
	d.accept(runtime.TurnStarted, "")
	d.accept(runtime.ModelDelta, "secr")
	d.accept(runtime.TurnCompleted, "")
	d.accept(runtime.TurnStarted, "")
	d.accept(runtime.ModelDelta, "et")
	d.accept(runtime.TurnCompleted, "")
	d.accept(runtime.TaskCompleted, "")
	if got.String() != "[REDACTED]" {
		t.Fatal("secret split across turns leaked", got.String())
	}
}

func TestTextStreamDoesNotDeliverUncommittedText(t *testing.T) {
	ctx := context.Background()
	db, err := telemetry.Open(ctx, t.TempDir()+"/journal.db")
	if err != nil {
		t.Fatal(err)
	}
	e := runtime.Event{Version: 1, ID: "start", TaskID: "task", SessionID: "session", CorrelationID: "task", Sequence: 1, Time: time.Now(), Kind: runtime.TaskStarted}
	if err := db.Append(ctx, 0, e); err != nil {
		t.Fatal(err)
	}
	e.ID, e.Sequence, e.Kind, e.TurnID = "turn", 2, runtime.TurnStarted, "turn"
	if err := db.Append(ctx, 1, e); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	delivery := &textDelivery{emit: func(string, bool) { t.Fatal("uncommitted text delivered") }}
	delivery.accept(runtime.TurnStarted, "")
	j := redactingJournal{db: db, textDelivery: delivery}
	e.ID, e.Sequence, e.Kind, e.Data.Text = "delta", 3, runtime.ModelDelta, "private provisional text"
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := j.Append(ctx, 2, e); err == nil {
		t.Fatal("closed storage accepted append")
	}
}
