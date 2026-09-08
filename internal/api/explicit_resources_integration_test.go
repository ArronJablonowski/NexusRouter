package api

import (
	"context"
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
)

func TestHTTPExplicitLocalTasksShareAutomaticResourceCapacity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" && r.Method == "GET" {
			fmt.Fprintln(w, `{"models":[{"name":"fixture"}]}`)
			return
		}
		if r.URL.Path != "/api/chat" || r.Method != "POST" {
			t.Error("unexpected provider request")
			http.Error(w, "unexpected", 500)
			return
		}
		if calls.Add(1) == 1 {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return
			}
		}
		fmt.Fprintln(w, `{"message":{"content":"answer"},"done":true,"done_reason":"stop"}`)
	}))
	defer provider.Close()
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Workers.Max = 2 // Application slots must not mask the resource gate.
	cfg.Hardware.Concurrent = "1"
	cfg.Hardware.MaxRAM = 100
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "shared.db")
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: provider.URL}}
	zero := 0.0
	// One byte is a synthetic HTTP fixture estimate, not real model guidance.
	cfg.Models = []config.Model{{ID: "chat", Provider: "local", Model: "fixture", Locality: "local", Capabilities: []string{"chat"}, RAMBytes: 1, ContextTokens: 8192, EstimatedCost: &zero}}
	svc, err := app.NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(token, 2, Services{Run: svc.Run, RunSubmission: fixedIdempotent(svc.Run),
		Inspect: func(context.Context, string) (sessions.Snapshot, error) { return sessions.Snapshot{}, nil },
		Health:  func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h)
	defer server.Close()
	defer cancel() // Stop a blocked fixture before deferred server shutdown.
	type response struct {
		code int
		body string
		err  error
	}
	post := func(model string) response {
		r, err := http.NewRequestWithContext(ctx, "POST", server.URL+"/v1/tasks", strings.NewReader(fmt.Sprintf(`{"model_id":%q,"prompt":"hello"}`, model)))
		if err != nil {
			return response{err: err}
		}
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", "resource-task-"+model+"-00000000")
		res, err := server.Client().Do(r)
		if err != nil {
			return response{err: err}
		}
		defer res.Body.Close()
		body, err := io.ReadAll(res.Body)
		return response{res.StatusCode, string(body), err}
	}
	first := make(chan response, 1)
	go func() { first <- post("chat") }()
	select {
	case <-entered:
	case out := <-first:
		t.Fatal("fixture task did not enter provider", out)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	for _, model := range []string{"chat", "auto"} {
		out := post(model)
		if out.err != nil || out.code != 422 || !strings.Contains(out.body, "admission_denied") || calls.Load() != 1 {
			t.Fatal("resource gate failed", model, out, calls.Load())
		}
	}
	close(release)
	out := <-first
	if out.err != nil || out.code != 201 {
		t.Fatal("first task failed", out)
	}
	for _, model := range []string{"chat", "auto"} {
		out := post(model)
		if out.err != nil || out.code != 201 {
			t.Fatal("reservation not released or counted twice", model, out)
		}
	}
	if calls.Load() != 3 {
		t.Fatal("unexpected inference count", calls.Load())
	}
}
