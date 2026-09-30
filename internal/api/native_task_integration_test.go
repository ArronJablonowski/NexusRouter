package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

func TestNativeTaskDurableRetrySurvivesDisconnectedWaiter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		calls.Add(1)
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-release:
			fmt.Fprintln(w, `{"message":{"content":"answer"},"done":true,"done_reason":"stop"}`)
		case <-r.Context().Done():
		}
	}))
	defer provider.Close()

	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Workers.Max = 1
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "native-task.db")
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: provider.URL}}
	cfg.Models = []config.Model{{ID: "chat", Provider: "local", Model: "fixture", Locality: "local", RAMBytes: 1, ContextTokens: 8192, Capabilities: []string{"chat"}}}
	svc, err := newAPIFixtureService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := app.StartDispatcher(ctx, svc)
	if err != nil {
		t.Fatal(err)
	}
	defer dispatcher.Close()
	h, err := New(token, 4, Services{
		Run:           svc.Run,
		RunSubmission: svc.RunSubmission,
		Inspect:       func(context.Context, string) (sessions.Snapshot, error) { return sessions.Snapshot{}, nil },
		Health:        func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h)
	defer server.Close()

	request := func(requestCtx context.Context, key, body string) (*http.Response, error) {
		r, err := http.NewRequestWithContext(requestCtx, http.MethodPost, server.URL+"/v1/tasks", strings.NewReader(body))
		if err != nil {
			return nil, err
		}
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", key)
		return server.Client().Do(r)
	}
	body := `{"model_id":"chat","prompt":"hello"}`
	key := "durable-native-task-0001"
	waitCtx, stopWait := context.WithCancel(ctx)
	disconnected := make(chan error, 1)
	go func() {
		response, err := request(waitCtx, key, body)
		if response != nil {
			response.Body.Close()
		}
		disconnected <- err
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("provider did not start")
	}
	stopWait()
	select {
	case err := <-disconnected:
		if err == nil {
			t.Fatal("canceled waiter unexpectedly received a response")
		}
	case <-ctx.Done():
		t.Fatal("canceled waiter did not return")
	}

	type outcome struct {
		code int
		body map[string]json.RawMessage
		err  error
	}
	retry := func() <-chan outcome {
		done := make(chan outcome, 1)
		go func() {
			response, err := request(ctx, key, body)
			if err != nil {
				done <- outcome{err: err}
				return
			}
			defer response.Body.Close()
			var decoded map[string]json.RawMessage
			err = json.NewDecoder(response.Body).Decode(&decoded)
			done <- outcome{code: response.StatusCode, body: decoded, err: err}
		}()
		return done
	}
	first, second := retry(), retry()
	close(release)
	a, b := <-first, <-second
	if a.err != nil || b.err != nil || a.code != http.StatusCreated || b.code != http.StatusCreated {
		t.Fatalf("retry results: %#v %#v", a, b)
	}
	if string(a.body["submission_id"]) == "" || string(a.body["submission_id"]) != string(b.body["submission_id"]) || string(a.body["task_id"]) != string(b.body["task_id"]) || string(a.body["text"]) != `"answer"` || calls.Load() != 1 {
		t.Fatalf("retries were not one durable execution: %#v %#v calls=%d", a.body, b.body, calls.Load())
	}

	changed, err := request(ctx, key, `{"model_id":"chat","prompt":"changed"}`)
	if err != nil {
		t.Fatal(err)
	}
	changedBody, _ := io.ReadAll(changed.Body)
	changed.Body.Close()
	if changed.StatusCode != http.StatusConflict || string(changedBody) != "{\"error\":\"task_conflict\"}\n" || calls.Load() != 1 {
		t.Fatalf("changed body: %d %q calls=%d", changed.StatusCode, changedBody, calls.Load())
	}

	denied, err := request(ctx, "durable-native-task-denied", `{"model_id":"missing","prompt":"hello"}`)
	if err != nil {
		t.Fatal(err)
	}
	deniedBody, _ := io.ReadAll(denied.Body)
	denied.Body.Close()
	var deniedResult map[string]json.RawMessage
	if denied.StatusCode != http.StatusUnprocessableEntity || json.Unmarshal(deniedBody, &deniedResult) != nil || string(deniedResult["error"]) != `"admission_denied"` || string(deniedResult["submission_id"]) == "" || calls.Load() != 1 {
		t.Fatalf("admission result: %d %q calls=%d", denied.StatusCode, deniedBody, calls.Load())
	}

	missing, err := request(ctx, "durable-missing-continuation", `{"model_id":"chat","prompt":"hello","continue_task_id":"missing-task"}`)
	if err != nil {
		t.Fatal(err)
	}
	missingBody, _ := io.ReadAll(missing.Body)
	missing.Body.Close()
	var missingResult map[string]json.RawMessage
	if missing.StatusCode != http.StatusUnprocessableEntity || json.Unmarshal(missingBody, &missingResult) != nil || string(missingResult["error"]) != `"admission_denied"` || calls.Load() != 1 {
		t.Fatalf("continuation admission: %d %q calls=%d", missing.StatusCode, missingBody, calls.Load())
	}
}
