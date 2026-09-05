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

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
	"github.com/ArronJablonowski/DarwinRouter/submissions"
)

func TestDetachedHTTPSubmissionSurvivesClientAndRetries(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	started, release := make(chan struct{}, 1), make(chan struct{})
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		calls.Add(1)
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-ctx.Done():
			return
		case <-r.Context().Done():
			return
		case <-release:
		}
		fmt.Fprintln(w, `{"message":{"content":"answer"},"done":true,"done_reason":"stop"}`)
	}))
	defer func() { cancel(); provider.Close() }()
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Workers.Max = 1
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "submissions.db")
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: provider.URL}}
	cfg.Models = []config.Model{{ID: "chat", Provider: "local", Model: "fixture", Locality: "local", RAMBytes: 1, Capabilities: []string{"chat"}}}
	svc, err := app.NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(token, 1, Services{Run: svc.Run, Submit: svc.Submit, Submission: svc.SubmissionStatus, CancelSubmission: svc.CancelSubmission,
		Inspect: func(context.Context, string) (sessions.Snapshot, error) { return sessions.Snapshot{}, nil },
		Health:  func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h)
	defer server.Close()
	request := func(method, path, body, key string) (submissions.Status, int) {
		t.Helper()
		requestCtx, stop := context.WithCancel(ctx)
		defer stop() // Disconnected submitter must not own the detached worker.
		r, _ := http.NewRequestWithContext(requestCtx, method, server.URL+path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		resp, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var status submissions.Status
		if json.NewDecoder(resp.Body).Decode(&status) != nil {
			t.Fatal("invalid response")
		}
		return status, resp.StatusCode
	}
	body := `{"model_id":"chat","prompt":"hello"}`
	key := "detached-request-1"
	first, code := request("POST", "/v1/submissions", body, key)
	if code != 202 || first.State != "queued" || first.ID == "" || calls.Load() != 0 {
		t.Fatal(code, first, calls.Load())
	}
	queued, code := request("POST", "/v1/submissions", body, "cancel-queued-request")
	if code != 202 {
		t.Fatal(code, queued)
	}
	stopped, code := request("POST", "/v1/submissions/"+queued.ID+"/cancel", `{}`, "")
	if code != 200 || stopped.State != "canceled" || !stopped.CancelRequested || stopped.Result != nil {
		t.Fatal(code, stopped)
	}
	// Starting a fresh service/dispatcher proves pending admission is in SQLite.
	restarted, err := app.NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := app.StartDispatcher(ctx, restarted)
	if err != nil {
		t.Fatal(err)
	}
	defer dispatcher.Close()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("queued request not dispatched")
	}
	again, code := request("POST", "/v1/submissions", body, key)
	if code != 202 || again.ID != first.ID || again.State != "running" || len(again.TaskIDs) != 1 {
		t.Fatal(code, again)
	}
	_, code = request("POST", "/v1/submissions", `{"model_id":"chat","prompt":"different"}`, key)
	if code != 409 {
		t.Fatal("changed intent reused key", code)
	}
	close(release)
	var done submissions.Status
	for {
		done, code = request("GET", "/v1/submissions/"+first.ID, "", "")
		if code != 200 {
			t.Fatal(code)
		}
		if done.State != "running" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("did not complete")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if done.State != "succeeded" || done.Result == nil || done.Result.Text != "answer" || calls.Load() != 1 {
		t.Fatal(done, calls.Load())
	}
	final, code := request("POST", "/v1/submissions", body, key)
	if code != 200 || final.ID != first.ID || final.State != "succeeded" || calls.Load() != 1 {
		t.Fatal(code, final)
	}
	if err := dispatcher.Close(); err != nil {
		t.Fatal(err)
	}
}
