package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	stdRuntime "runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

// Actual read_file runs, but its result never commits. One boundary keeps the
// child reader held; the other occurs after normal release. Process death, not
// lease expiry, permits failure-only recovery without re-reading the file.
func TestPendingReadOnlyWorkerAfterSIGKILLRecoversFailure(t *testing.T) {
	if stdRuntime.GOOS != "darwin" && stdRuntime.GOOS != "linux" {
		t.Skip("SIGKILL requires Unix")
	}
	for _, boundary := range []string{"before_read_result", "before_read_release"} {
		t.Run(boundary, func(t *testing.T) { qualifyPendingReadCrash(t, boundary) })
	}
}

func qualifyPendingReadCrash(t *testing.T, boundary string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	root := t.TempDir()
	note := filepath.Join(root, "note.txt")
	const private = "discarded-private-read-content"
	if err := os.WriteFile(note, []byte(private), 0600); err != nil {
		t.Fatal(err)
	}
	var parents, children atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Model string }
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Error("invalid fixture request")
			return
		}
		switch request.Model {
		case "parent":
			if parents.Add(1) != 1 {
				t.Error("parent redispatched")
			}
			fmt.Fprintln(w, `{"message":{"tool_calls":[{"function":{"name":"delegate","arguments":{"prompt":"Read note.txt","validation":"text"}}}]},"done":true,"done_reason":"tool_calls"}`)
		case "child":
			if children.Add(1) != 1 {
				t.Error("child redispatched")
			}
			fmt.Fprintln(w, `{"message":{"tool_calls":[{"function":{"name":"read_file","arguments":{"path":"note.txt"}}}]},"done":true,"done_reason":"tool_calls"}`)
		default:
			t.Error("unknown fixture model")
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer provider.Close()
	cfg := config.Defaults()
	cfg.Mode, cfg.Workers.Max, cfg.Hardware.Concurrent = "local_only", 2, "2"
	cfg.Workers.DelegateModel, cfg.Workers.DelegateMaxCalls = "child", 1
	cfg.Workers.DelegateReadTools, cfg.Workers.DelegateMaxTurns = true, 4
	cfg.Memory.Enabled, cfg.Skills.Enabled = false, false
	cfg.Tools.Enabled, cfg.Tools.ReadRoot = true, root
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "pending-read.db")
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
	if err != nil || status.State != "running" || len(status.TaskIDs) != 3 || parents.Load() != 1 || children.Load() != 1 {
		t.Fatal("unexpected killed tree", err)
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
	parent, child := "", ""
	for _, task := range status.TaskIDs {
		page, err := db.ReadEventPage(ctx, task, 0, 100)
		if err != nil || page.HasMore || page.State != "running" || len(page.Events) < 2 {
			t.Fatal("invalid source journal", err)
		}
		journals[task] = page.Events
		if page.Events[0].Data.ParentTaskID == "" {
			parent = task
		}
		for _, event := range page.Events {
			if strings.Contains(event.Data.Text, private) {
				t.Fatal("read result already committed")
			}
			if event.Kind == runtime.ToolStarted && event.Data.ToolName == "read_file" {
				child = task
				if event.Data.ToolBehavior != runtime.BehaviorReadOnly {
					t.Fatal("read contract absent")
				}
			}
		}
	}
	if parent == "" || child == "" {
		t.Fatal("missing bound pending read")
	}
	initial, err := db.TaskSnapshot(ctx, child)
	if err != nil || len(initial.Pending) != 1 || !initial.UncertainEffects || initial.InterruptedTurn {
		t.Fatal("no pending read at crash", err)
	}
	var total, released int
	if err := raw.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(released),0) FROM resource_leases WHERE task_id=? AND scope='workspace' AND writer=0`, child).Scan(&total, &released); err != nil || total != 1 {
		t.Fatal("missing actual read lease", err)
	}
	wantReleased := 1
	if boundary == "before_read_release" {
		wantReleased = 0
	}
	if released != wantReleased {
		t.Fatal("wrong read lease crash boundary")
	}
	if err := os.Remove(note); err != nil {
		t.Fatal(err)
	} // owned fixture only; no possible successful rerun.
	expireRecoveryClaim(t, svc, id)
	qualifyOrphanChildSweep(t, ctx, svc, db, id, parent, journals, "interrupted_read_only_tool")
	if parents.Load() != 1 || children.Load() != 1 {
		t.Fatal("recovery invoked models")
	}
	if err := raw.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(released),0) FROM resource_leases WHERE task_id=? AND scope='workspace' AND writer=0`, child).Scan(&total, &released); err != nil || total != 1 || released != 1 {
		t.Fatal("read replayed or retained", err)
	}
	final, err := db.TaskSnapshot(ctx, child)
	if err != nil || len(final.Messages) == 0 || !final.Messages[len(final.Messages)-1].ToolFailed || strings.Contains(final.Messages[len(final.Messages)-1].Content, private) {
		t.Fatal("unknown read result presented as success", err)
	}
}
