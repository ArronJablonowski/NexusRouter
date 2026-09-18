package app

import (
	"context"
	"database/sql"
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

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/resources"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/tools"
)

func workerFinishFixture(t *testing.T, cancelProvider context.CancelFunc) (*Service, config.Settings, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		calls.Add(1)
		if cancelProvider != nil {
			cancelProvider()
			select {
			case <-r.Context().Done():
			case <-time.After(2 * time.Second):
			}
			return
		}
		fmt.Fprintln(w, `{"message":{"content":"accepted worker answer"},"done":true,"done_reason":"stop"}`)
	}))
	t.Cleanup(server.Close)
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "finish.db")
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: server.URL}}
	zero := 0.0
	cfg.Models = []config.Model{{ID: "child", Model: "child", Provider: "local", Locality: "local", RAMBytes: 1, ContextTokens: 8192, EstimatedCost: &zero, Capabilities: []string{"chat"}}}
	svc, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc.profile = func(context.Context) (resources.Snapshot, error) {
		return resources.Snapshot{Time: time.Now(), CPUs: 4, TotalRAM: 1000, AvailableRAM: 1000}, nil
	}
	return svc, cfg, &calls
}

// The production parent's public stream intentionally excludes child journals.
// Bind the production redacting journal's sink directly to observe the exact
// commit boundary while the delegated execution still runs a real HTTP model.
func TestWorkerFinishApplicationJournalPublishesOnlyAfterLeaseRelease(t *testing.T) {
	for _, mode := range []string{"success", "canceled", "release_rejected", "sink_panic"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			var providerCancel context.CancelFunc
			if mode == "canceled" {
				providerCancel = cancel
			}
			svc, cfg, calls := workerFinishFixture(t, providerCancel)
			db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if mode == "release_rejected" {
				fixture, err := sql.Open("sqlite", cfg.Telemetry.Database)
				if err != nil {
					t.Fatal(err)
				}
				defer fixture.Close()
				if _, err = fixture.Exec(`CREATE TRIGGER reject_worker_release BEFORE UPDATE OF released ON resource_leases WHEN OLD.scope='delegation-parent' AND NEW.released=1 BEGIN SELECT RAISE(ABORT,'fixture release denied'); END`); err != nil {
					t.Fatal(err)
				}
			}
			var joined atomic.Bool
			var terminal atomic.Int32
			var workID string
			journal := redactingJournal{db: db, eventSink: func(e runtime.Event) {
				if e.Kind == runtime.TaskStarted {
					workID = e.TaskID
				}
				if e.Kind != runtime.TaskCompleted && e.Kind != runtime.TaskCanceled && e.Kind != runtime.TaskFailed {
					return
				}
				terminal.Add(1)
				if !joined.Load() {
					t.Error("terminal observed before callback joined")
				}
				leases, err := db.InspectLeases(context.Background(), "delegation-parent")
				if err != nil || len(leases) != 0 {
					t.Error("terminal visible before atomic reader release", err, len(leases))
				}
				if mode == "canceled" && e.Kind != runtime.TaskCanceled {
					t.Error("wrong cancellation terminal", e.Kind)
				}
				if mode == "sink_panic" {
					panic("private callback failure")
				}
			}}
			registry := &tools.Registry{}
			err = registerDelegate(registry, nil, db, journal, cfg, "parent", "session", "", true, func(ctx context.Context, prompt, validation, work string, _ bool) (Result, error) {
				defer joined.Store(true)
				return svc.Run(ctx, Request{ModelID: "child", Prompt: prompt, Validation: validation, delegatedParent: work})
			}, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			executor := scopedDelegateTestExecutor{tools.Executor{Registry: registry, Policy: &tools.Policy{Default: tools.Allow}}}
			out, runErr := executor.Execute(ctx, delegateSafetyCall(`{"prompt":"bounded worker request","validation":"text"}`))
			if calls.Load() != 1 || !joined.Load() {
				t.Fatal("child did not run and join", calls.Load())
			}
			snapshot, err := db.TaskSnapshot(context.Background(), workID)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "success" {
				if runErr != nil || out.Failed || !strings.Contains(out.Content, "accepted worker answer") || terminal.Load() != 1 || snapshot.State != "completed" {
					t.Fatal("success not durably finalized", runErr, out, terminal.Load(), snapshot.State)
				}
			} else if mode == "canceled" {
				if !out.Failed || strings.Contains(out.Content, "accepted worker answer") || terminal.Load() != 1 || snapshot.State != "canceled" {
					t.Fatal("canceled work accepted", runErr, terminal.Load(), snapshot.State)
				}
			} else if mode == "sink_panic" {
				if strings.Contains(out.Content, "accepted worker answer") || strings.Contains(out.Content, "private callback failure") || terminal.Load() != 1 || snapshot.State != "completed" {
					t.Fatal("callback panic changed completion or exposed output", runErr, out, terminal.Load(), snapshot.State)
				}
			} else {
				if strings.Contains(out.Content, "accepted worker answer") || terminal.Load() != 0 || snapshot.State != "running" {
					t.Fatal("failed atomic finalization accepted output", runErr, out, terminal.Load(), snapshot.State)
				}
				leases, err := db.InspectLeases(context.Background(), "delegation-parent")
				if err != nil || len(leases) != 1 {
					t.Fatal("failed transaction lost ownership", err, len(leases))
				}
			}
		})
	}
}

func TestWorkerFinishFailureCannotReachActualParentModel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	var parentCalls, childCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model    string              `json:"model"`
			Messages []providers.Message `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Error("invalid fixture request")
			return
		}
		if request.Model == "child" {
			childCalls.Add(1)
			fmt.Fprintln(w, `{"message":{"content":"private accepted child output"},"done":true,"done_reason":"stop"}`)
			return
		}
		if parentCalls.Add(1) == 1 {
			fmt.Fprintln(w, `{"message":{"tool_calls":[{"function":{"name":"delegate","arguments":{"prompt":"bounded task","validation":"text"}}}]},"done":true,"done_reason":"tool_calls"}`)
			return
		}
		for _, m := range request.Messages {
			if strings.Contains(m.Content, "private accepted child output") {
				t.Error("unfinalized child output reached parent inference")
			}
		}
		fmt.Fprintln(w, `{"message":{"content":"rejected work acknowledged"},"done":true,"done_reason":"stop"}`)
	}))
	defer server.Close()
	_, cfg, _ := workerFinishFixture(t, nil)
	cfg.Providers[0].Endpoint = server.URL
	cfg.Workers.DelegateModel = "child"
	cfg.Workers.DelegateMaxCalls = 1
	cfg.Workers.Max = 2
	cfg.Hardware.Concurrent = "2"
	parent := cfg.Models[0]
	parent.ID, parent.Model = "parent", "parent"
	cfg.Models = append(cfg.Models, parent)
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	fixture, err := sql.Open("sqlite", cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.Close()
	if _, err = fixture.Exec(`CREATE TRIGGER reject_worker_release BEFORE UPDATE OF released ON resource_leases WHEN OLD.scope GLOB 'delegation-*' AND NEW.released=1 BEGIN SELECT RAISE(ABORT,'fixture release denied'); END`); err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc.profile = func(context.Context) (resources.Snapshot, error) {
		return resources.Snapshot{Time: time.Now(), CPUs: 4, TotalRAM: 1000, AvailableRAM: 1000}, nil
	}
	result, _ := svc.Run(ctx, Request{ModelID: "parent", Prompt: "delegate bounded task"})
	if childCalls.Load() != 1 || strings.Contains(result.Text, "private accepted child output") {
		t.Fatal("unfinalized child accepted", childCalls.Load())
	}
	leases, err := db.InspectLeases(ctx, "delegation-"+result.TaskID)
	if err != nil || len(leases) != 1 {
		t.Fatal("failed finish lost live ownership", err, len(leases))
	}
	work, err := db.TaskSnapshot(ctx, leases[0].TaskID)
	if err != nil || work.State != "running" {
		t.Fatal("failed finish committed terminal state", err, work.State)
	}
}
