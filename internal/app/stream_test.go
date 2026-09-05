package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"darwinrouter/internal/config"
	"darwinrouter/internal/telemetry"
	"darwinrouter/runtime"
)

func streamService(t *testing.T, handler http.HandlerFunc) (*Service, string) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "stream.db")
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: server.URL, APIKeyEnv: "FIXTURE_KEY"}}
	cfg.Models = []config.Model{{ID: "chat", Provider: "local", Model: "fixture", Locality: "local", Capabilities: []string{"chat"}}}
	s, err := NewService(cfg, func(string) string { return "fixture-secret" })
	if err != nil {
		t.Fatal(err)
	}
	return s, cfg.Telemetry.Database
}

func TestRunStreamDeliversCommittedRedactedEvents(t *testing.T) {
	s, path := streamService(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, `{"message":{"content":"fixture-"},"done":false}`)
		fmt.Fprintln(w, `{"message":{"content":"secret answer"},"done":true,"done_reason":"stop"}`)
	})
	var delivered []runtime.Event
	out, err := s.RunStream(context.Background(), Request{ModelID: "chat", Prompt: "avoid fixture-secret"}, func(e runtime.Event) error {
		db, err := telemetry.OpenReadOnly(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		stored, err := db.Read(context.Background(), e.TaskID, e.Sequence-1, 1)
		db.Close()
		if err != nil || len(stored) != 1 || stored[0].ID != e.ID {
			t.Fatal("callback preceded commit", stored, err)
		}
		want, _ := stored[0].Encode()
		got, _ := e.Encode()
		if string(want) != string(got) {
			t.Fatal("callback differs from committed event")
		}
		if strings.Contains(string(got), "fixture-secret") || (e.Kind == runtime.ModelDelta && e.Data.Text != "") {
			t.Fatal("raw secret delivered", string(got))
		}
		if e.Sequence != int64(len(delivered)+1) {
			t.Fatal("event order", e.Sequence)
		}
		delivered = append(delivered, e)
		return nil
	})
	if err != nil || out.Text != "[REDACTED] answer" || len(delivered) == 0 || delivered[0].Kind != runtime.TaskStarted || delivered[len(delivered)-1].Kind != runtime.TaskCompleted {
		t.Fatal(out, delivered, err)
	}
}

func TestRunStreamSinkFailureCancelsDurably(t *testing.T) {
	for _, mode := range []string{"error", "panic", "start"} {
		t.Run(mode, func(t *testing.T) {
			providerCanceled := make(chan struct{}, 1)
			s, path := streamService(t, func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintln(w, `{"message":{"content":"partial"},"done":false}`)
				w.(http.Flusher).Flush()
				<-r.Context().Done()
				providerCanceled <- struct{}{}
			})
			calls := 0
			stopped := false
			out, err := s.RunStream(context.Background(), Request{ModelID: "chat", Prompt: "hello"}, func(e runtime.Event) error {
				calls++
				if stopped {
					t.Fatal("delivery continued after failure")
				}
				if e.Kind == runtime.ModelDelta || mode == "start" {
					stopped = true
					if mode == "panic" {
						panic("private panic")
					}
					return errors.New("private callback error")
				}
				return nil
			})
			if !errors.Is(err, ErrEventDelivery) || errors.Is(err, runtime.ErrPersistence) || strings.Contains(err.Error(), "private") || out.TaskID == "" || calls == 0 {
				t.Fatal(out, calls, err)
			}
			if mode != "start" {
				select {
				case <-providerCanceled:
				case <-time.After(time.Second):
					t.Fatal("provider not canceled")
				}
			}
			db, openErr := telemetry.OpenReadOnly(context.Background(), path)
			if openErr != nil {
				t.Fatal(openErr)
			}
			defer db.Close()
			events, readErr := db.Read(context.Background(), out.TaskID, 0, 100)
			if readErr != nil || len(events) < 2 || events[len(events)-1].Kind != runtime.TaskCanceled {
				t.Fatal("missing durable cancellation", events, readErr)
			}
		})
	}
}

func TestRunStreamNilSinkAndFailedPersistence(t *testing.T) {
	var absent *Service
	if _, err := absent.RunStream(context.Background(), Request{}, nil); !errors.Is(err, ErrAdmission) {
		t.Fatal("nil sink not rejected before effects", err)
	}
	db, err := telemetry.Open(context.Background(), filepath.Join(t.TempDir(), "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	j := redactingJournal{db: db, eventSink: func(runtime.Event) { t.Fatal("uncommitted event delivered") }}
	e := runtime.Event{Version: 1, ID: "start", TaskID: "task", SessionID: "session", CorrelationID: "task", Sequence: 1, Time: time.Now(), Kind: runtime.TaskStarted}
	if err := j.Append(context.Background(), 0, e); err == nil {
		t.Fatal("persistence failure ignored")
	}
}

func TestStreamCallbackCannotMutateCommittedData(t *testing.T) {
	db, err := telemetry.Open(context.Background(), filepath.Join(t.TempDir(), "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	e := runtime.Event{Version: 1, ID: "start", TaskID: "task", SessionID: "session", CorrelationID: "task", Sequence: 1, Time: time.Now(), Kind: runtime.TaskStarted}
	if err := json.Unmarshal([]byte(`{"messages":[{"role":"user","content":"original"}]}`), &e.Data); err != nil {
		t.Fatal(err)
	}
	j := redactingJournal{db: db, eventSink: func(event runtime.Event) { event.Data.Messages[0].Content = "mutated" }}
	if err := j.Append(context.Background(), 0, e); err != nil {
		t.Fatal(err)
	}
	if e.Data.Messages[0].Content != "original" {
		t.Fatal("callback mutated caller's source")
	}
	stored, err := db.Read(context.Background(), "task", 0, 1)
	if err != nil || stored[0].Data.Messages[0].Content != "original" {
		t.Fatal(stored, err)
	}
}

func TestRunStreamFallbackEventOrdering(t *testing.T) {
	svc, _ := autoFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			fmt.Fprintln(w, `{"models":[{"name":"a"},{"name":"z"}]}`)
			return
		}
		var request struct{ Model string }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if request.Model == "a" {
			w.WriteHeader(503)
			return
		}
		fmt.Fprintln(w, `{"message":{"content":"fallback answer"},"done":true,"done_reason":"stop"}`)
	}))
	defer server.Close()
	svc.settings.Providers[0].Endpoint = server.URL
	var events []runtime.Event
	out, err := svc.RunStream(context.Background(), Request{Prompt: "hello"}, func(e runtime.Event) error { events = append(events, e); return nil })
	if err != nil || len(out.PreviousTaskIDs) != 1 || out.Text != "fallback answer" {
		t.Fatal(out, err)
	}
	first, second, previousSequence := out.PreviousTaskIDs[0], false, int64(0)
	for i, e := range events {
		if e.TaskID == out.TaskID && !second {
			if i == 0 || events[i-1].Kind != runtime.TaskFailed || e.Kind != runtime.TaskStarted || e.Data.RetryOfTaskID != first {
				t.Fatal("fallback began before committed failure", events)
			}
			second, previousSequence = true, 0
		}
		if (!second && e.TaskID != first) || (second && e.TaskID != out.TaskID) || e.Sequence != previousSequence+1 {
			t.Fatal("interleaved or unordered events", events)
		}
		previousSequence = e.Sequence
	}
	if !second || events[len(events)-1].Kind != runtime.TaskCompleted {
		t.Fatal(events)
	}
}
