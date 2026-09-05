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

	"darwinrouter/internal/app"
	"darwinrouter/internal/config"
	"darwinrouter/internal/telemetry"
	"darwinrouter/runtime"
	"darwinrouter/sessions"
)

func TestHTTPSteeringAppliesAtNextTurnAndPersistsStatus(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	const guidance = "Use the revised requirement"
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Error("invalid provider body")
			return
		}
		n := calls.Add(1)
		answer := "initial answer"
		if n == 1 {
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			case <-ctx.Done():
				return
			}
		} else if n == 2 {
			if len(request.Messages) < 3 || request.Messages[len(request.Messages)-1].Role != "user" || request.Messages[len(request.Messages)-1].Content != guidance {
				t.Error("second turn missing user steering", request.Messages)
			}
			answer = "revised answer"
		} else {
			t.Error("unexpected extra inference", n)
		}
		fmt.Fprintf(w, `{"message":{"content":%q},"done":true,"done_reason":"stop"}`, answer)
	}))
	defer func() { cancel(); provider.Close() }()
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "steering.db")
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: provider.URL}}
	cfg.Models = []config.Model{{ID: "chat", Provider: "local", Model: "fixture", Locality: "local", RAMBytes: 1, Capabilities: []string{"chat"}}}
	svc, err := app.NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	hooks := services()
	hooks.Run = svc.Run
	hooks.Steer = svc.SteerTask
	hooks.Steering = svc.SteeringStatus
	h, err := New(token, 1, hooks)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h)
	defer server.Close()
	started := make(chan string, 1)
	type outcome struct {
		result app.Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := svc.RunStream(ctx, app.Request{ModelID: "chat", Prompt: "Original task"}, func(e runtime.Event) error {
			if e.Kind == runtime.TaskStarted {
				started <- e.TaskID
			}
			return nil
		})
		done <- outcome{result, err}
	}()
	var task string
	select {
	case task = <-started:
	case result := <-done:
		t.Fatal("run did not start", result)
	case <-ctx.Done():
		t.Fatal("no durable start")
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("provider did not start")
	}
	control := func(method, path, body string) (int, map[string]json.RawMessage) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), guidance) || strings.Contains(string(data), "private-key") {
			t.Fatal("control echoed request", string(data))
		}
		var out map[string]json.RawMessage
		if json.Unmarshal(data, &out) != nil {
			t.Fatal(string(data))
		}
		return response.StatusCode, out
	}
	path := "/v1/tasks/" + task + "/steering"
	status, accepted := control("POST", path, `{"idempotency_key":"private-key","text":"`+guidance+`"}`)
	if status != 202 {
		t.Fatal(status, accepted)
	}
	var id string
	if json.Unmarshal(accepted["id"], &id) != nil || id == "" {
		t.Fatal(accepted)
	}
	close(release)
	select {
	case result := <-done:
		if result.err != nil || result.result.Text != "revised answer" || result.result.Turns != 2 {
			t.Fatal(result)
		}
	case <-ctx.Done():
		t.Fatal("steered run did not finish")
	}
	status, applied := control("GET", path+"/"+id, "")
	var sequence int64
	if status != 200 || string(applied["state"]) != `"applied"` || json.Unmarshal(applied["applied_sequence"], &sequence) != nil || sequence < 2 || calls.Load() != 2 {
		t.Fatal(status, applied, calls.Load())
	}
	db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	snapshot, err := sessions.Replay(ctx, db, task)
	if err != nil || snapshot.State != "completed" || len(snapshot.Messages) < 4 || snapshot.Messages[len(snapshot.Messages)-2].Content != guidance || snapshot.MessageSequences[len(snapshot.Messages)-2] != sequence {
		t.Fatal(snapshot, err)
	}
}
