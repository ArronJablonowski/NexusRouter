package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func TestDurableTaskStreamFallbackUsesOneGlobalReplayOrder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var callsMu sync.Mutex
	var calls []string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			fmt.Fprintln(w, `{"models":[{"name":"a"},{"name":"z"}]}`)
			return
		}
		var request struct {
			Model string `json:"model"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Error("invalid provider request")
			return
		}
		callsMu.Lock()
		calls = append(calls, request.Model)
		callsMu.Unlock()
		if request.Model == "a" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintln(w, `{"message":{"content":"fallback answer"},"done":true,"done_reason":"stop"}`)
	}))
	defer provider.Close()

	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Routing.Exploration = 0
	cfg.Workers.Max = 1
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "fallback-stream.db")
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: provider.URL}}
	zero := 0.0
	for _, id := range []string{"a", "z"} {
		cfg.Models = append(cfg.Models, config.Model{ID: id, Provider: "local", Model: id, Locality: "local", RAMBytes: 1, ContextTokens: 8192, Capabilities: []string{"chat"}, EstimatedCost: &zero})
	}
	svc, err := app.NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := app.StartDispatcher(ctx, svc)
	if err != nil {
		t.Fatal(err)
	}
	defer dispatcher.Close()
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h, err := New(token, 2, Services{
		Run:              svc.Run,
		Submit:           svc.Submit,
		ResumeSubmission: svc.ResumeSubmission,
		SubmissionStream: db.ReadSubmissionStreamPage,
		Inspect:          func(ctx context.Context, id string) (sessions.Snapshot, error) { return sessions.Replay(ctx, db, id) },
		Health:           func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h)
	defer server.Close()
	body, key := `{"model_id":"auto","prompt":"hello"}`, "fallback-stream-key-0001"
	request := func(cursor string) (*http.Response, error) {
		r, requestErr := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/tasks/stream", strings.NewReader(body))
		if requestErr != nil {
			return nil, requestErr
		}
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", key)
		if cursor != "" {
			r.Header.Set("Last-Event-ID", cursor)
		}
		return server.Client().Do(r)
	}
	response, err := request("")
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatal(response, err)
	}
	encoded, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	frames := parseStreamFrames(t, string(encoded))
	if len(frames) < 7 || frames[len(frames)-1].event != "result" {
		t.Fatal("incomplete fallback stream", frames)
	}
	var starts []runtime.Event
	failedAt := -1
	for i, frame := range frames {
		if !strings.HasSuffix(frame.id, fmt.Sprintf(":%d", i+1)) {
			t.Fatal("non-contiguous global cursor", i, frame.id)
		}
		if frame.event == "result" {
			continue
		}
		var event runtime.Event
		if json.Unmarshal([]byte(frame.data), &event) != nil || event.Validate() != nil {
			t.Fatal("invalid runtime frame", frame)
		}
		if event.Kind == runtime.TaskStarted {
			starts = append(starts, event)
		}
		if event.Kind == runtime.TaskFailed {
			failedAt = i
		}
	}
	if len(starts) != 2 || failedAt < 0 || starts[0].TaskID == starts[1].TaskID || starts[1].Data.RetryOfTaskID != starts[0].TaskID || failedAt >= len(frames)-1 {
		t.Fatal("fallback lineage missing from global stream", starts, failedAt)
	}
	var result struct {
		Text            string   `json:"text"`
		TaskID          string   `json:"task_id"`
		PreviousTaskIDs []string `json:"previous_task_ids"`
	}
	if json.Unmarshal([]byte(frames[len(frames)-1].data), &result) != nil || result.Text != "fallback answer" || result.TaskID != starts[1].TaskID || !reflect.DeepEqual(result.PreviousTaskIDs, []string{starts[0].TaskID}) {
		t.Fatal("wrong fallback result", result)
	}
	callsMu.Lock()
	gotCalls := append([]string(nil), calls...)
	callsMu.Unlock()
	if !reflect.DeepEqual(gotCalls, []string{"a", "z"}) {
		t.Fatal("wrong provider attempts", gotCalls)
	}

	resumed, err := request(frames[failedAt].id)
	if err != nil || resumed.StatusCode != http.StatusOK {
		t.Fatal(resumed, err)
	}
	resumedBody, _ := io.ReadAll(resumed.Body)
	resumed.Body.Close()
	resumedFrames := parseStreamFrames(t, string(resumedBody))
	wantFrames := frames[failedAt+1:]
	if !reflect.DeepEqual(resumedFrames, wantFrames) {
		t.Fatalf("fallback resume mismatch\n got=%+v\nwant=%+v", resumedFrames, wantFrames)
	}
}

func TestDurableTaskStreamAdmissionFailureHasReplayableResultOnly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Workers.Max = 1
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "denied-stream.db")
	svc, err := app.NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := app.StartDispatcher(ctx, svc)
	if err != nil {
		t.Fatal(err)
	}
	defer dispatcher.Close()
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h, err := New(token, 1, Services{
		Run:              svc.Run,
		Submit:           svc.Submit,
		ResumeSubmission: svc.ResumeSubmission,
		SubmissionStream: db.ReadSubmissionStreamPage,
		Inspect:          func(context.Context, string) (sessions.Snapshot, error) { return sessions.Snapshot{}, nil },
		Health:           func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h)
	defer server.Close()
	body, key := `{"model_id":"missing","prompt":"hello"}`, "denied-stream-key-00001"
	request := func(cursor string) (*http.Response, error) {
		r, requestErr := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/tasks/stream", strings.NewReader(body))
		if requestErr != nil {
			return nil, requestErr
		}
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", key)
		if cursor != "" {
			r.Header.Set("Last-Event-ID", cursor)
		}
		return server.Client().Do(r)
	}
	response, err := request("")
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatal(response, err)
	}
	encoded, _ := io.ReadAll(response.Body)
	response.Body.Close()
	frames := parseStreamFrames(t, string(encoded))
	if len(frames) != 1 || frames[0].event != "result" || !strings.HasSuffix(frames[0].id, ":1") || !strings.Contains(frames[0].data, `"error":"admission_denied"`) {
		t.Fatal("admission result was not durable", frames)
	}
	submissionID, _, ok := strings.Cut(frames[0].id, ":")
	if !ok {
		t.Fatal(frames[0].id)
	}
	resumed, err := request(submissionID + ":0")
	if err != nil || resumed.StatusCode != http.StatusOK {
		t.Fatal(resumed, err)
	}
	resumedBody, _ := io.ReadAll(resumed.Body)
	resumed.Body.Close()
	if string(resumedBody) != string(encoded) {
		t.Fatalf("admission replay changed: %q != %q", resumedBody, encoded)
	}
}

func TestDurableTaskStreamTaskFailureReplaysAfterRestartWithoutRedispatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var calls int
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			fmt.Fprintln(w, `{"models":[{"name":"a"}]}`)
			return
		}
		calls++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))

	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Workers.Max = 1
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "failed-stream.db")
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: provider.URL}}
	cfg.Models = []config.Model{{ID: "a", Provider: "local", Model: "a", Locality: "local", RAMBytes: 1, ContextTokens: 8192, Capabilities: []string{"chat"}}}
	svc, err := app.NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := app.StartDispatcher(ctx, svc)
	if err != nil {
		t.Fatal(err)
	}
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	makeHandler := func(service *app.Service, store *telemetry.Store) *Handler {
		h, handlerErr := New(token, 1, Services{
			Run:              service.Run,
			Submit:           service.Submit,
			ResumeSubmission: service.ResumeSubmission,
			SubmissionStream: store.ReadSubmissionStreamPage,
			Inspect:          func(context.Context, string) (sessions.Snapshot, error) { return sessions.Snapshot{}, nil },
			Health:           func(context.Context) error { return nil },
		})
		if handlerErr != nil {
			t.Fatal(handlerErr)
		}
		return h
	}
	server := httptest.NewServer(makeHandler(svc, db))
	body, key := `{"model_id":"a","prompt":"hello"}`, "failed-stream-key-00001"
	call := func(baseURL, cursor string) (*http.Response, error) {
		r, requestErr := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/v1/tasks/stream", strings.NewReader(body))
		if requestErr != nil {
			return nil, requestErr
		}
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", key)
		if cursor != "" {
			r.Header.Set("Last-Event-ID", cursor)
		}
		return http.DefaultClient.Do(r)
	}
	response, err := call(server.URL, "")
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatal(response, err)
	}
	encoded, _ := io.ReadAll(response.Body)
	response.Body.Close()
	frames := parseStreamFrames(t, string(encoded))
	failedAt := -1
	for i, frame := range frames {
		if frame.event == string(runtime.TaskFailed) {
			failedAt = i
		}
	}
	if failedAt < 0 || len(frames) != failedAt+2 || frames[len(frames)-1].event != "result" || !strings.Contains(frames[len(frames)-1].data, `"error":"task_failed"`) || strings.Contains(frames[len(frames)-1].data, `"text"`) || calls != 1 {
		t.Fatal("ordinary failure was not safely streamed", frames, calls)
	}
	terminalID := frames[len(frames)-1].id
	failedID := frames[failedAt].id
	server.Close()
	if err := dispatcher.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	provider.Close()

	restartedSvc, err := app.NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	restartedDB, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer restartedDB.Close()
	restarted := httptest.NewServer(makeHandler(restartedSvc, restartedDB))
	defer restarted.Close()
	replayed, err := call(restarted.URL, failedID)
	if err != nil || replayed.StatusCode != http.StatusOK {
		t.Fatal(replayed, err)
	}
	replayedBody, _ := io.ReadAll(replayed.Body)
	replayed.Body.Close()
	replayedFrames := parseStreamFrames(t, string(replayedBody))
	if len(replayedFrames) != 1 || replayedFrames[0].event != "result" || replayedFrames[0].id != terminalID || calls != 1 {
		t.Fatal("failed result did not replay without redispatch", replayedFrames, calls)
	}
}
