package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"darwinrouter/internal/app"
	"darwinrouter/internal/config"
	"darwinrouter/internal/telemetry"
	"darwinrouter/runtime"
	"darwinrouter/sessions"
)

func TestHTTPDurableCancellationWithFullExecutionCapacity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	started, stopped := make(chan struct{}), make(chan struct{})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		select {
		case <-r.Context().Done():
		case <-ctx.Done():
		}
		close(stopped)
	}))
	defer func() { cancel(); provider.Close() }()
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "cancellation.db")
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: provider.URL}}
	cfg.Models = []config.Model{{ID: "chat", Provider: "local", Model: "fixture", Locality: "local", RAMBytes: 1, Capabilities: []string{"chat"}}}
	runner, err := app.NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	// A separate application service has no private handle to the runner's
	// context. Cancellation must travel through committed database state.
	controller, err := app.NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h, err := New(token, 1, Services{Run: runner.Run, RunStream: runner.RunStream, Cancel: controller.CancelTask, Cancellation: controller.CancellationStatus,
		Inspect: func(ctx context.Context, id string) (sessions.Snapshot, error) { return sessions.Replay(ctx, db, id) },
		Health:  func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h)
	defer server.Close()
	req, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/v1/tasks/stream", strings.NewReader(`{"model_id":"chat","prompt":"hello"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal(response.Status)
	}
	scanner := bufio.NewScanner(response.Body)
	var first runtime.Event
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "data: ") {
			if json.Unmarshal([]byte(strings.TrimPrefix(scanner.Text(), "data: ")), &first) != nil {
				t.Fatal(scanner.Text())
			}
			break
		}
	}
	if first.Kind != runtime.TaskStarted {
		t.Fatal("missing active task", first, scanner.Err())
	}
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("provider did not start")
	}
	if len(h.slots) != 1 {
		t.Fatal("execution capacity should be full")
	}
	control := func(method, suffix string) (runtime.CancellationStatus, int) {
		t.Helper()
		var body io.Reader
		if method == "POST" {
			body = strings.NewReader(`{}`)
		}
		r, _ := http.NewRequestWithContext(ctx, method, server.URL+"/v1/tasks/"+first.TaskID+suffix, body)
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		resp, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var state runtime.CancellationStatus
		if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
			t.Fatal(err)
		}
		return state, resp.StatusCode
	}
	accepted, status := control("POST", "/cancel")
	if status != 202 || !accepted.Requested || accepted.RequestID == "" || accepted.RequestedAt == nil {
		t.Fatal("cancellation not admitted", status, accepted)
	}
	persisted, err := db.CancellationStatus(ctx, first.TaskID)
	if err != nil || persisted.RequestID != accepted.RequestID {
		t.Fatal("acknowledged before durable request", persisted, err)
	}
	repeated, status := control("POST", "/cancel")
	if (status != 200 && status != 202) || repeated.RequestID != accepted.RequestID || !repeated.RequestedAt.Equal(*accepted.RequestedAt) {
		t.Fatal("cancellation retry changed identity", status, repeated)
	}
	var remaining strings.Builder
	for scanner.Scan() {
		remaining.WriteString(scanner.Text())
		remaining.WriteByte('\n')
	}
	if scanner.Err() != nil {
		t.Fatal(scanner.Err())
	}
	if !strings.Contains(remaining.String(), "event: task.canceled") || !strings.Contains(remaining.String(), `"error":"canceled"`) {
		t.Fatal("missing canceled lifecycle/result", remaining.String())
	}
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("provider not canceled")
	}
	shown, status := control("GET", "/cancellation")
	if status != 200 || shown.State != "canceled" || shown.RequestID != accepted.RequestID {
		t.Fatal(shown, status)
	}
	snapshot, err := sessions.Replay(ctx, db, first.TaskID)
	if err != nil || snapshot.State != "canceled" {
		t.Fatal(snapshot, err)
	}
	// A fresh connection sees both immutable control identity and terminal log.
	ro, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	restored, err := ro.CancellationStatus(ctx, first.TaskID)
	if err != nil || restored.State != "canceled" || restored.RequestID != accepted.RequestID {
		t.Fatal(restored, err)
	}
}
