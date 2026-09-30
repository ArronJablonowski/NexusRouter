package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	stdRuntime "runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

// A real built-in read completes before the next inference is killed. Recovery
// must preserve that call/result pair and fail the interrupted inference rather
// than redispatching the provider or rereading the file.
func TestReadOnlyWorkerAfterSIGKILLRecoversFailure(t *testing.T) {
	if stdRuntime.GOOS != "darwin" && stdRuntime.GOOS != "linux" {
		t.Skip("SIGKILL qualification requires Unix")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	note := filepath.Join(workspace, "note.txt")
	if err := os.WriteFile(note, []byte("read-only crash evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	started, disconnected := make(chan struct{}, 1), make(chan struct{}, 1)
	var parents, children atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model    string                           `json:"model"`
			Messages []struct{ Role, Content string } `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid provider request")
			return
		}
		if body.Model == "chat" {
			if parents.Add(1) != 1 {
				t.Error("parent redispatched")
			}
			fmt.Fprintln(w, `{"message":{"tool_calls":[{"function":{"name":"delegate","arguments":{"prompt":"Read note.txt","validation":"text"}}}]},"done":true,"done_reason":"tool_calls"}`)
			return
		}
		if body.Model != "child" {
			t.Error("unknown model")
			return
		}
		n := children.Add(1)
		if n == 1 {
			fmt.Fprintln(w, `{"message":{"tool_calls":[{"function":{"name":"read_file","arguments":{"path":"note.txt"}}}]},"done":true,"done_reason":"tool_calls"}`)
			return
		}
		if n != 2 {
			t.Error("child redispatched")
		}
		if len(body.Messages) == 0 || body.Messages[len(body.Messages)-1].Role != "tool" || !strings.Contains(body.Messages[len(body.Messages)-1].Content, "read-only crash evidence") {
			t.Error("next inference lacks completed read evidence")
		}
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
		case <-ctx.Done():
			return
		}
		select {
		case disconnected <- struct{}{}:
		default:
		}
	}))
	defer func() { cancel(); provider.Close() }()
	cfg := config.Defaults()
	cfg.Mode, cfg.Workers.Max, cfg.Hardware.Concurrent = "local_only", 3, "3"
	cfg.Workers.DelegateModel, cfg.Workers.DelegateMaxCalls = "child", 1
	cfg.Workers.DelegateReadTools, cfg.Workers.DelegateMaxTurns = true, 4
	cfg.Tools.Enabled, cfg.Tools.ReadRoot = true, workspace
	cfg.Telemetry.Database = filepath.Join(root, "read-crash.db")
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: provider.URL}}
	zero := 0.0
	for _, id := range []string{"chat", "child"} {
		cfg.Models = append(cfg.Models, config.Model{ID: id, Model: id, Provider: "local", Locality: "local", RAMBytes: 1, ContextTokens: 16384, EstimatedCost: &zero, Capabilities: []string{"chat"}})
	}
	body, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	configuration := filepath.Join(root, "config.json")
	if err := os.WriteFile(configuration, body, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDelegateTreeProcessHelper$", "-test.count=1")
	cmd.Env = []string{"DARWIN_PROCESS_OWNER_DIR=" + filepath.Join(root, "owners"), "DARWIN_DELEGATE_TREE_HELPER=1", "DARWIN_DELEGATE_TREE_CONFIG=" + configuration}
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("second child inference never started")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	waited = true
	if err == nil || cmd.ProcessState == nil {
		t.Fatal("helper not killed")
	}
	if state, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); !ok || !state.Signaled() || state.Signal() != syscall.SIGKILL {
		t.Fatal("helper did not terminate by SIGKILL")
	}
	select {
	case <-disconnected:
	case <-ctx.Done():
		t.Fatal("child inference remained connected")
	}
	svc, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	page, err := db.ListSubmissions(ctx, submissions.ListOptions{State: "running", Limit: 10})
	if err != nil || len(page.Items) != 1 || page.HasMore || len(page.Items[0].TaskIDs) != 3 {
		t.Fatal("unexpected intake", err)
	}
	status := page.Items[0]
	journals := map[string][]runtime.Event{}
	parent, child := "", ""
	for _, id := range status.TaskIDs {
		snapshot, err := db.TaskSnapshot(ctx, id)
		if err != nil || snapshot.State != "running" {
			t.Fatal("unexpected task state", err)
		}
		events, err := db.Read(ctx, id, 0, 100)
		if err != nil || int64(len(events)) != snapshot.Sequence {
			t.Fatal(err)
		}
		journals[id] = events
		if snapshot.ParentTaskID == "" {
			parent = id
		}
		for _, e := range events {
			if e.Kind == runtime.ToolStarted && e.Data.ToolName == "read_file" {
				child = id
			}
		}
	}
	if parent == "" || child == "" {
		t.Fatal("missing parent or read child")
	}
	var start, end *runtime.Event
	for i := range journals[child] {
		e := &journals[child][i]
		if e.Kind == runtime.ToolStarted {
			if start != nil {
				t.Fatal("repeated read")
			}
			start = e
		}
		if e.Kind == runtime.ToolCompleted {
			if end != nil {
				t.Fatal("repeated read completion")
			}
			end = e
		}
	}
	if start == nil || end == nil || start.Data.ToolBehavior != runtime.BehaviorReadOnly || end.Data.ToolBehavior != runtime.BehaviorReadOnly || end.Data.Effect != runtime.NoEffect || start.Data.ToolCallID != end.Data.ToolCallID || !strings.Contains(end.Data.Text, "read-only crash evidence") {
		t.Fatal("missing explicit paired read-only result")
	}
	raw, err := sql.Open("sqlite", cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var readTotal, readReleased int
	if err := raw.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(released),0) FROM resource_leases WHERE task_id=? AND scope='workspace' AND writer=0`, child).Scan(&readTotal, &readReleased); err != nil || readTotal != 1 || readReleased != 1 {
		t.Fatal("read lease not released before next inference", err, readTotal, readReleased)
	}
	// The input disappears after its successful read. Recovery must not execute
	// another read, and the original recorded evidence must remain unchanged.
	if err := os.Remove(note); err != nil {
		t.Fatal(err)
	}
	expireRecoveryClaim(t, svc, status.ID)
	qualifyOrphanChildSweep(t, ctx, svc, db, status.ID, parent, journals, "interrupted_read_only_model")
	if parents.Load() != 1 || children.Load() != 2 {
		t.Fatal("recovery redispatched models", parents.Load(), children.Load())
	}
	if err := raw.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(released),0) FROM resource_leases WHERE task_id=? AND scope='workspace' AND writer=0`, child).Scan(&readTotal, &readReleased); err != nil || readTotal != 1 || readReleased != 1 {
		t.Fatal("recovery repeated read or changed released lease", err)
	}
}
