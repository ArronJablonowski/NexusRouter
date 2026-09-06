package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	stdRuntime "runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

// Interrupt the actual application inside the child's first INSERT, after
// worker start/reader ownership committed. SIGKILL rolls back child creation;
// recovery must fail existing work, never create or execute the missing child.
func TestWorkerWithoutChildAfterSIGKILLRecoversFailure(t *testing.T) {
	if stdRuntime.GOOS != "darwin" && stdRuntime.GOOS != "linux" {
		t.Skip("SIGKILL qualification requires Unix")
	}
	for _, boundary := range []string{"before_child", "before_worker_started"} {
		t.Run(boundary, func(t *testing.T) { qualifyWorkerWithoutChildCrash(t, boundary) })
	}
}

func qualifyWorkerWithoutChildCrash(t *testing.T, boundary string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var parents, children atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Model string }
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Error("invalid fixture request")
			return
		}
		if request.Model == "parent" {
			if parents.Add(1) != 1 {
				t.Error("parent redispatched")
			}
			fmt.Fprintln(w, `{"message":{"tool_calls":[{"function":{"name":"delegate","arguments":{"prompt":"bounded work","validation":"text"}}}]},"done":true,"done_reason":"tool_calls"}`)
			return
		}
		children.Add(1)
		t.Error("child inference dispatched despite pre-child crash")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer provider.Close()
	cfg := config.Defaults()
	cfg.Mode, cfg.Workers.Max, cfg.Hardware.Concurrent = "local_only", 2, "2"
	cfg.Workers.DelegateModel, cfg.Workers.DelegateMaxCalls = "child", 1
	cfg.Memory.Enabled, cfg.Skills.Enabled, cfg.Tools.Enabled = false, false, false
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "before-child.db")
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: provider.URL}}
	zero := 0.0
	for _, id := range []string{"parent", "child"} {
		cfg.Models = append(cfg.Models, config.Model{ID: id, Model: id, Provider: "local", Locality: "local", RAMBytes: 1, ContextTokens: 16384, EstimatedCost: &zero, Capabilities: []string{"chat"}})
	}
	id := killWorkerFinishFixture(t, ctx, cfg, boundary)
	svc, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	status, err := svc.SubmissionStatus(ctx, id)
	if err != nil || status.State != "running" || len(status.TaskIDs) != 2 || parents.Load() != 1 || children.Load() != 0 {
		t.Fatal("crash boundary did not leave exactly parent+worker", err)
	}
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	raw, err := sql.Open("sqlite", cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.ExecContext(ctx, `DROP TRIGGER crash_before_parent_tool_result`); err != nil {
		t.Fatal(err)
	}
	journals := map[string][]runtime.Event{}
	parent, worker := "", ""
	for _, task := range status.TaskIDs {
		page, err := db.ReadEventPage(ctx, task, 0, 100)
		if err != nil || page.HasMore || page.State != "running" || len(page.Events) < 1 {
			t.Fatal("unexpected crash journal", err)
		}
		journals[task] = page.Events
		start, end := page.Events[0], page.Events[len(page.Events)-1]
		if start.WorkerID == "" {
			parent = task
			if start.Data.ParentTaskID != "" || end.Kind != runtime.ToolStarted || end.Data.ToolName != "delegate" || end.Data.ToolBehavior != runtime.BehaviorReadOnly {
				t.Fatal("unbound parent dispatch")
			}
		} else {
			worker = task
			count, kind := 2, runtime.WorkerStarted
			if boundary == "before_worker_started" {
				count, kind = 1, runtime.TaskStarted
			}
			if len(page.Events) != count || end.Kind != kind {
				t.Fatal("worker progressed before interrupted child creation")
			}
		}
	}
	if parent == "" || worker == "" || journals[worker][0].Data.ParentTaskID != parent {
		t.Fatal("missing worker origin")
	}
	assertNoChild := func() {
		t.Helper()
		var count int
		if err := raw.QueryRowContext(ctx, `SELECT count(*) FROM events WHERE json_extract(body,'$.data.parent_task_id')=?`, worker).Scan(&count); err != nil || count != 0 {
			t.Fatal("child linkage created", count, err)
		}
	}
	assertNoChild()
	p, err := db.InspectLeases(ctx, "delegation")
	if err != nil || len(p) != 1 || p[0].TaskID != parent || p[0].Writer {
		t.Fatal("missing parent reader", err)
	}
	w, err := db.InspectLeases(ctx, "delegation-"+parent)
	if err != nil || len(w) != 1 || w[0].TaskID != worker || w[0].Writer {
		t.Fatal("missing worker reader", err)
	}
	expireRecoveryClaim(t, svc, id) // claim expiry permits reconciliation, not death proof.
	qualifyOrphanWorkerSweep(t, ctx, svc, db, raw, id, p[0], w[0], journals, "orphan_worker_without_child_unlocked")
	assertNoChild()
	after, err := svc.SubmissionStatus(ctx, id)
	if err != nil || len(after.TaskIDs) != 2 || after.State != "failed" || parents.Load() != 1 || children.Load() != 0 {
		t.Fatal("recovery dispatched or invented missing work", err)
	}
	page, err := db.ReadEventPage(ctx, parent, 0, 100)
	if err != nil || page.HasMore {
		t.Fatal(err)
	}
	var rejection struct {
		Work  string `json:"work_task_id"`
		Child string `json:"execution_task_id"`
	}
	if json.Unmarshal([]byte(page.Events[len(journals[parent])].Data.Text), &rejection) != nil || rejection.Work != worker || rejection.Child != "" {
		t.Fatal("parent rejection invented child evidence")
	}
}
