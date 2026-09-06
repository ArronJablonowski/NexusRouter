package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	stdRuntime "runtime"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/submissions"
)

// An interrupted provider request supplies no proof of completion. Manual
// journal repair cannot change it; verified process-ownership proof permits
// failure only, never redispatch or invented completed inference.
func TestUnfinishedDelegationAfterSIGKILLRecoversFailure(t *testing.T) {
	if stdRuntime.GOOS != "darwin" && stdRuntime.GOOS != "linux" {
		t.Skip("SIGKILL qualification requires Unix")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	started, disconnected := make(chan struct{}, 1), make(chan struct{}, 1)
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body struct {
			Model string `json:"model"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid provider request")
			return
		}
		if body.Model == "child" {
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
			return
		}
		fmt.Fprintln(w, `{"message":{"tool_calls":[{"function":{"name":"delegate","arguments":{"prompt":"unfinished","validation":"text"}}}]},"done":true,"done_reason":"tool_calls"}`)
	}))
	defer provider.Close()
	// Ensure a failed assertion cannot leave the fixture handler blocking Close.
	defer cancel()
	cfg := config.Defaults()
	cfg.Mode, cfg.Workers.Max, cfg.Hardware.Concurrent = "local_only", 3, "3"
	cfg.Workers.DelegateModel, cfg.Workers.DelegateMaxCalls = "child", 2
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "unfinished.db")
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: provider.URL}}
	zero := 0.0
	for _, id := range []string{"chat", "child"} {
		cfg.Models = append(cfg.Models, config.Model{ID: id, Model: id, Provider: "local", Locality: "local", RAMBytes: 1, ContextTokens: 16384, EstimatedCost: &zero, Capabilities: []string{"chat"}})
	}
	body, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDelegateTreeProcessHelper$", "-test.count=1")
	cmd.Env = []string{"DARWIN_DELEGATE_TREE_HELPER=1", "DARWIN_DELEGATE_TREE_CONFIG=" + path}
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
		t.Fatal("child provider was not dispatched")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	waited = true
	if err == nil || cmd.ProcessState == nil {
		t.Fatal("helper was not killed")
	}
	if state, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); !ok || !state.Signaled() || state.Signal() != syscall.SIGKILL {
		t.Fatal("helper did not terminate by SIGKILL")
	}
	select {
	case <-disconnected:
	case <-ctx.Done():
		t.Fatal("killed provider request remained connected")
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
	if err != nil || len(page.Items) != 1 || page.HasMore {
		t.Fatal("unexpected intake", err)
	}
	before := page.Items[0]
	if len(before.TaskIDs) != 3 || calls.Load() != 2 {
		t.Fatal("wrong dispatched tree", calls.Load())
	}
	journals := map[string][]runtime.Event{}
	parent := ""
	for _, id := range before.TaskIDs {
		snapshot, err := db.TaskSnapshot(ctx, id)
		if err != nil || snapshot.State != "running" {
			t.Fatal("unexpected terminal state", err)
		}
		if snapshot.ParentTaskID == "" {
			parent = id
			if len(snapshot.Pending) != 1 || !snapshot.UncertainEffects {
				t.Fatal("parent lost pending delegation")
			}
			for _, pending := range snapshot.Pending {
				if !pending.Dispatched || pending.Call.Name != "delegate" {
					t.Fatal("unexpected pending tool boundary")
				}
			}
		}
		events, err := db.Read(ctx, id, 0, 100)
		if err != nil || len(events) == 0 || int64(len(events)) != snapshot.Sequence {
			t.Fatal(err)
		}
		journals[id] = events
	}
	if parent == "" {
		t.Fatal("missing parent")
	}
	expireRecoveryClaim(t, svc, before.ID)
	dispatcher := &Dispatcher{db: db}
	for i := 0; i < 2; i++ {
		if _, err := dispatcher.recoverPage(ctx, svc.submissionConfigDigest(), ""); err != nil {
			t.Fatal(err)
		}
	}
	after, err := svc.SubmissionStatus(ctx, before.ID)
	if err != nil || after.State != "running" || !after.LeaseExpired || after.Result != nil || !reflect.DeepEqual(before.TaskIDs, after.TaskIDs) || calls.Load() != 2 {
		t.Fatal("unfinished work was repaired or dispatched", err)
	}
	for id, original := range journals {
		events, err := db.Read(ctx, id, 0, 100)
		if err != nil || !reflect.DeepEqual(original, events) {
			t.Fatal("unfinished journal changed", id, err)
		}
	}
	receipts, err := db.RecoveryHistory(ctx, before.ID)
	if err != nil || len(receipts) != 0 {
		t.Fatal("invented recovery receipt", err)
	}
	if _, err := loadContinuation(ctx, db, Request{ContinueTaskID: parent}, nil); !errors.Is(err, ErrAdmission) {
		t.Fatal("unfinished delegation admitted as restored history", err)
	}
	qualifyOrphanChildSweep(t, ctx, svc, db, before.ID, parent, journals)
	if calls.Load() != 2 {
		t.Fatal("orphan child recovery redispatched provider", calls.Load())
	}
}
