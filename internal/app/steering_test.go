package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

func TestSteeringAcrossServicesReachesNextTurnAndReplays(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, cfg := autoFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []providers.Message `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("bad provider request")
			return
		}
		if calls.Add(1) == 1 {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return
			}
			fmt.Fprintln(w, `{"message":{"content":"original answer"},"done":true,"done_reason":"stop"}`)
		} else {
			if len(body.Messages) != 3 || body.Messages[1].Role != "assistant" || body.Messages[2].Role != "user" || body.Messages[2].Content != "Use [REDACTED] as the revised topic" {
				t.Errorf("guidance order/redaction: %+v", body.Messages)
			}
			fmt.Fprintln(w, `{"message":{"content":"revised answer"},"done":true,"done_reason":"stop"}`)
		}
	}))
	defer func() { cancel(); server.Close() }()
	s.settings.Providers[0].Endpoint = server.URL
	s.secret = func(string) string { return "private-token" }
	control, err := NewService(s.settings, s.secret)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan string, 1)
	type completion struct {
		result Result
		err    error
	}
	done := make(chan completion, 1)
	go func() {
		out, err := s.RunStream(ctx, Request{ModelID: "a", Prompt: "first question"}, func(e runtime.Event) error {
			if e.Kind == runtime.TaskStarted {
				started <- e.TaskID
			}
			return nil
		})
		done <- completion{out, err}
	}()
	var task string
	select {
	case task = <-started:
	case <-ctx.Done():
		t.Fatal("task start timeout")
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("provider start timeout")
	}
	message, err := control.SteerTask(ctx, task, "opaque-key", "Use private-token as the revised topic")
	if err != nil || message.State != "pending" || strings.Contains(message.Text, "private-token") {
		t.Fatal(message, err)
	}
	duplicate, err := control.SteerTask(ctx, task, "opaque-key", "Use private-token as the revised topic")
	if err != nil || duplicate.ID != message.ID {
		t.Fatal(duplicate, err)
	}
	if _, err := control.SteerTask(ctx, task, "opaque-key", "different"); !errors.Is(err, telemetry.ErrConflict) {
		t.Fatal("key conflict", err)
	}
	close(release)
	select {
	case out := <-done:
		if out.err != nil || out.result.Text != "revised answer" || out.result.Turns != 2 || calls.Load() != 2 {
			t.Fatal(out, calls.Load())
		}
	case <-ctx.Done():
		t.Fatal("completion timeout")
	}
	status, err := control.SteeringStatus(ctx, task, message.ID)
	if err != nil || status.State != "applied" || status.AppliedSequence == nil {
		t.Fatal(status, err)
	}
	if _, err := control.SteerTask(ctx, task, "late-key", "too late"); !errors.Is(err, runtime.ErrSteeringClosed) {
		t.Fatal(err)
	}
	if same, err := control.SteerTask(ctx, task, "opaque-key", "Use private-token as the revised topic"); err != nil || same.ID != message.ID || same.State != "applied" {
		t.Fatal(same, err)
	}
	db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	history, err := sessions.Replay(ctx, db, task)
	if err != nil || history.State != "completed" || len(history.Messages) != 4 || history.Messages[2].Content != message.Text {
		t.Fatal(history, err)
	}
}
