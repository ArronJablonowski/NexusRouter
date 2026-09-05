package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func TestHTTPEventReplayDisconnectLeavesActiveTaskRunning(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := make(chan struct{})
	release := make(chan struct{})
	providerCanceled := make(chan struct{})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		select {
		case <-ctx.Done():
			return
		case <-r.Context().Done():
			close(providerCanceled)
			return
		case <-release:
			if r.Context().Err() != nil {
				close(providerCanceled)
				return
			}
			fmt.Fprintln(w, `{"message":{"content":"answer"},"done":true,"done_reason":"stop"}`)
		}
	}))
	defer func() { cancel(); provider.Close() }()
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "active.db")
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: provider.URL}}
	cfg.Models = []config.Model{{ID: "chat", Provider: "local", Model: "fixture", Locality: "local", RAMBytes: 1, Capabilities: []string{"chat"}}}
	svc, err := app.NewService(cfg, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	taskStarted := make(chan string, 1)
	type runOutcome struct {
		result app.Result
		err    error
	}
	done := make(chan runOutcome, 1)
	go func() {
		result, err := svc.RunStream(ctx, app.Request{ModelID: "chat", Prompt: "hello"}, func(e runtime.Event) error {
			if e.Kind == runtime.TaskStarted {
				taskStarted <- e.TaskID
			}
			return nil
		})
		done <- runOutcome{result, err}
	}()
	var taskID string
	select {
	case taskID = <-taskStarted:
	case <-ctx.Done():
		t.Fatal("missing durable task start")
	}
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("provider did not start")
	}
	db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h, err := New(token, 2, Services{Run: svc.Run, Events: db.ReadEventPage,
		Inspect: func(ctx context.Context, id string) (sessions.Snapshot, error) { return sessions.Replay(ctx, db, id) },
		Health:  func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h)
	defer server.Close()
	// Replay has a distinct cancellation lifetime from execution.
	replayCtx, replayCancel := context.WithCancel(ctx)
	defer replayCancel()
	get := func() *http.Response {
		t.Helper()
		req, _ := http.NewRequestWithContext(replayCtx, http.MethodGet, server.URL+"/v1/tasks/"+taskID+"/events", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			t.Fatal(response.Status)
		}
		return response
	}
	response := get()
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	frames := parseStreamFrames(t, string(body))
	if len(frames) < 2 || len(frames) > 101 {
		t.Fatal("expected bounded active page", len(frames))
	}
	var checkpoint struct {
		State string `json:"state"`
	}
	last := frames[len(frames)-1]
	if last.event != "checkpoint" || json.Unmarshal([]byte(last.data), &checkpoint) != nil || checkpoint.State != "running" {
		t.Fatal("missing running snapshot", last)
	}
	// Interrupt a second replay after its first frame, without draining it.
	response = get()
	scanner := bufio.NewScanner(response.Body)
	if !scanner.Scan() {
		response.Body.Close()
		t.Fatal("missing replay frame", scanner.Err())
	}
	response.Body.Close()
	replayCancel()
	select {
	case <-providerCanceled:
		t.Fatal("replay canceled the active provider")
	default:
	}
	close(release)
	select {
	case outcome := <-done:
		if outcome.err != nil || outcome.result.TaskID != taskID || outcome.result.Text != "answer" {
			t.Fatal("execution did not survive replay disconnect", outcome)
		}
	case <-ctx.Done():
		t.Fatal("execution did not finish")
	}
	snapshot, err := sessions.Replay(ctx, db, taskID)
	if err != nil || snapshot.State != "completed" {
		t.Fatal("missing durable completion", snapshot.State, err)
	}
}
