package app

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/codexbridge"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/resources"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

// This exercises the real application/HTTP worker and durable cancellation
// path with a fixture coordinator. It does not qualify live Codex descendants.
func TestCodexTaskCancellationDuringLocalDelegation(t *testing.T) {
	for _, durable := range []bool{false, true} {
		name := "caller_context"
		if durable {
			name = "durable_request"
		}
		t.Run(name, func(t *testing.T) {
			ctx, stop := context.WithTimeout(context.Background(), 8*time.Second)
			defer stop()
			runCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			entered, disconnected, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) != 1 {
					t.Error("worker was dispatched more than once")
					return
				}
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "application/x-ndjson")
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
				close(entered)
				select {
				case <-r.Context().Done():
					close(disconnected)
				case <-release:
				}
			}))
			defer func() { cancel(); close(release); local.Close() }()
			cfg := codexTaskConfig(t)
			cfg.Workers.Max, cfg.Workers.DelegateMaxCalls = 2, 1
			cfg.Workers.DelegateModel = "worker"
			zero := 0.0
			cfg.Providers = append(cfg.Providers, config.Provider{ID: "local", Kind: "ollama", Endpoint: local.URL})
			cfg.Models = append(cfg.Models, config.Model{ID: "worker", Provider: "local", Model: "fixture-local", Locality: "local", RAMBytes: 1, ContextTokens: 4096, EstimatedCost: &zero, Capabilities: []string{"chat"}})
			svc, err := NewService(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			svc.profile = func(context.Context) (resources.Snapshot, error) {
				return resources.Snapshot{Time: time.Now(), CPUs: 4, TotalRAM: 8 << 30, AvailableRAM: 7 << 30}, nil
			}
			provider := &codexTaskFixture{}
			var dir string
			svc.codexLauncher = func(_ context.Context, spec codexbridge.LaunchSpec) (taskProvider, error) {
				dir = spec.CWD
				return provider, nil
			}
			parentID := make(chan string, 1)
			done := make(chan error, 1)
			joined := make(chan struct{})
			defer func() {
				cancel()
				select {
				case <-joined:
				case <-time.After(time.Second):
					t.Error("fixture task did not join during cleanup")
				}
			}()
			go func() {
				defer close(joined)
				_, err := svc.Run(runCtx, Request{ModelID: "brain", Prompt: "Delegate then review", eventSink: func(e runtime.Event) {
					if e.Kind == runtime.TaskStarted && e.Data.ParentTaskID == "" {
						parentID <- e.TaskID
					}
				}})
				done <- err
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("worker did not start")
			}
			var task string
			select {
			case task = <-parentID:
			case <-ctx.Done():
				t.Fatal("parent not observed")
			}
			if durable {
				// A fresh service instance must control an already-running task
				// through durable state, not a private in-memory cancel handle.
				control, err := NewService(cfg, nil)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := control.CancelTask(ctx, task); err != nil {
					t.Fatal(err)
				}
			} else {
				cancel()
			}
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation lost: %v", err)
				}
			case <-ctx.Done():
				t.Fatal("cancellation did not join task")
			}
			select {
			case <-disconnected:
			case <-ctx.Done():
				t.Fatal("worker HTTP request retained")
			}
			if calls.Load() != 1 || provider.calls != 1 || provider.closed != 1 || provider.result != "" {
				t.Fatal("canceled worker returned or repeated work")
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatal("private coordinator directory retained")
			}
			verifyCanceledCodexTree(t, ctx, cfg.Telemetry.Database, task)
		})
	}
}

func verifyCanceledCodexTree(t *testing.T, ctx context.Context, path, parent string) {
	t.Helper()
	db, err := telemetry.OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	raw, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	rows, err := raw.QueryContext(ctx, `SELECT DISTINCT task_id FROM events`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count, found := 0, false
	rejection := false
	var rejectionBody string
	parents := map[string]string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		count++
		found = found || id == parent
		page, err := db.ReadEventPage(ctx, id, 0, 100)
		if err != nil || page.State != "canceled" || page.HasMore {
			t.Fatalf("tree task not canceled: %s %s %v", id, page.State, err)
		}
		for _, e := range page.Events {
			if e.Kind == runtime.TaskStarted {
				parents[id] = e.Data.ParentTaskID
			}
			if e.Kind == runtime.TaskCompleted || e.Kind == runtime.WorkerCompleted {
				t.Fatal("canceled tree recorded accepted output")
			}
			if e.Kind == runtime.ToolCompleted && (id != parent || e.Data.Effect != runtime.NoEffect) {
				t.Fatal("canceled delegate did not retain bounded rejection")
			}
			if e.Kind == runtime.ToolCompleted && id == parent && e.Data.ToolName == "delegate" {
				rejection = true
				rejectionBody = e.Data.Text
			}
		}
	}
	if rows.Err() != nil || count != 3 || !found || !rejection {
		t.Fatal("incomplete cancellation tree", count, rows.Err())
	}
	work, execution := "", ""
	for id, owner := range parents {
		if owner == parent {
			work = id
		}
	}
	for id, owner := range parents {
		if owner == work {
			execution = id
		}
	}
	if parents[parent] != "" || work == "" || execution == "" || execution == parent {
		t.Fatal("cancellation lost parent/work/execution attribution")
	}
	verifyCodexDelegateRejection(t, ctx, db, rejectionBody, "canceled", work, execution)
}
