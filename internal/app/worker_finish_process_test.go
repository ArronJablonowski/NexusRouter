package app

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	stdRuntime "runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

// Exercise both sides of worker finalization in the actual application, not a
// hand-authored terminal history. Preliminary acceptance cannot replace the
// terminal commit, and its lease is distinct from the enclosing tool reader.
func TestWorkerFinalizationProcessDeathRecoveryBoundary(t *testing.T) {
	if stdRuntime.GOOS != "darwin" && stdRuntime.GOOS != "linux" {
		t.Skip("SIGKILL qualification requires Unix")
	}
	for _, boundary := range []string{"worker_release", "parent_release"} {
		t.Run(boundary, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
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
						t.Error("parent inference repeated across recovery")
						w.WriteHeader(500)
						return
					}
					fmt.Fprintln(w, `{"message":{"tool_calls":[{"function":{"name":"delegate","arguments":{"prompt":"bounded work","validation":"text"}}}]},"done":true,"done_reason":"tool_calls"}`)
				case "child":
					children.Add(1)
					fmt.Fprintln(w, `{"message":{"content":"durable worker candidate"},"done":true,"done_reason":"stop"}`)
				default:
					t.Error("unexpected fixture model")
					w.WriteHeader(500)
				}
			}))
			defer provider.Close()
			cfg := config.Defaults()
			cfg.Mode, cfg.Workers.Max, cfg.Hardware.Concurrent = "local_only", 2, "2"
			cfg.Workers.DelegateModel, cfg.Workers.DelegateMaxCalls = "child", 1
			cfg.Memory.Enabled, cfg.Skills.Enabled, cfg.Tools.Enabled = false, false, false
			cfg.Telemetry.Database = filepath.Join(t.TempDir(), "finish-crash.db")
			cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: provider.URL}}
			zero := 0.0
			for _, id := range []string{"parent", "child"} {
				cfg.Models = append(cfg.Models, config.Model{ID: id, Model: id, Provider: "local", Locality: "local", RAMBytes: 1, ContextTokens: 16384, EstimatedCost: &zero, Capabilities: []string{"chat"}})
			}
			id := killWorkerFinishFixture(t, ctx, cfg, boundary)
			// No fixture-owned writer survives this point. Reopen after verified
			// SIGKILL so deferred cleanup cannot influence observed durability.
			svc, err := NewService(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			status, err := svc.SubmissionStatus(ctx, id)
			if err != nil || status.State != "running" || len(status.TaskIDs) != 3 || parents.Load() != 1 || children.Load() != 1 {
				t.Fatal("wrong pre-crash task tree", err)
			}
			db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			journals := map[string][]runtime.Event{}
			parent, work := "", ""
			for _, task := range status.TaskIDs {
				page, err := db.ReadEventPage(ctx, task, 0, 100)
				if err != nil || page.HasMore || len(page.Events) == 0 {
					t.Fatal("incomplete crash history", err)
				}
				journals[task] = page.Events
				start, end := page.Events[0], page.Events[len(page.Events)-1]
				if start.Data.ParentTaskID == "" {
					parent = task
					if page.State != "running" || end.Kind != runtime.ToolStarted {
						t.Fatal("parent tool result committed before crash")
					}
				} else if start.Data.DelegationOrigin != nil {
					work = task
					accepted := false
					for _, e := range page.Events {
						accepted = accepted || e.Kind == runtime.EvaluationRecorded && e.Data.Accepted != nil && *e.Data.Accepted
					}
					if !accepted {
						t.Fatal("crash occurred before preliminary worker acceptance")
					}
					if boundary == "worker_release" {
						if page.State != "running" || end.Kind != runtime.WorkerCompleted {
							t.Fatal("worker terminal did not roll back with interrupted release")
						}
					} else if page.State != "completed" || end.Kind != runtime.TaskCompleted {
						t.Fatal("worker terminal missing after committed finalization")
					}
				} else if page.State != "completed" || end.Kind != runtime.TaskCompleted {
					t.Fatal("child execution was not already completed")
				}
			}
			if parent == "" || work == "" || journals[work][0].Data.ParentTaskID != parent {
				t.Fatal("missing correlated worker")
			}
			raw, err := sql.Open("sqlite", cfg.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			if _, err = raw.ExecContext(ctx, `DROP TRIGGER crash_before_parent_tool_result`); err != nil {
				t.Fatal(err)
			}
			// Expiry is an explicit fixture manipulation, not a production death
			// detector. Both kinds of unresolved reader must remain blockers.
			if _, err = raw.ExecContext(ctx, `UPDATE resource_leases SET expires=1 WHERE released=0`); err != nil {
				t.Fatal(err)
			}
			workerLeases, err := db.InspectLeases(ctx, "delegation-"+parent)
			wantWorkers := 0
			if boundary == "worker_release" {
				wantWorkers = 1
			}
			if err != nil || len(workerLeases) != wantWorkers || wantWorkers == 1 && workerLeases[0].TaskID != work {
				t.Fatal("worker terminal and lease release disagree", err)
			}
			parentLeases, err := db.InspectLeases(ctx, "delegation")
			if err != nil || len(parentLeases) != 1 || parentLeases[0].TaskID != parent || parentLeases[0].Writer {
				t.Fatal("enclosing reader lease disappeared", err)
			}
			if _, err = db.AcquireLease(ctx, parent, "test-conflicting-writer", "delegation", true, time.Now(), time.Second); !errors.Is(err, telemetry.ErrLeaseBusy) {
				t.Fatal("expiry alone admitted a writer", err)
			}
			if boundary == "worker_release" {
				if _, err = db.AcquireLease(ctx, parent, "test-conflicting-worker-writer", "delegation-"+parent, true, time.Now(), time.Second); !errors.Is(err, telemetry.ErrLeaseBusy) {
					t.Fatal("expired unfinished worker no longer excludes a writer", err)
				}
			}
			expireRecoveryClaim(t, svc, id)
			dispatcher := &Dispatcher{db: db}
			var recoveredParent []runtime.Event
			var firstReceipts []byte
			for attempt := 0; attempt < 2; attempt++ {
				if _, err = dispatcher.recoverPage(ctx, svc.submissionConfigDigest(), ""); err != nil {
					t.Fatal(err)
				}
				after, err := svc.SubmissionStatus(ctx, id)
				if err != nil || !reflect.DeepEqual(status.TaskIDs, after.TaskIDs) {
					t.Fatal("recovery changed task membership", err)
				}
				receipts, err := db.RecoveryHistory(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				receiptBody, err := json.Marshal(receipts)
				if err != nil {
					t.Fatal(err)
				}
				if attempt == 0 {
					firstReceipts = receiptBody
				} else if string(firstReceipts) != string(receiptBody) {
					t.Fatal("repeat recovery changed receipt contents")
				}
				if boundary == "worker_release" {
					if after.State != "running" || after.Result != nil || len(receipts) != 0 {
						t.Fatal("preliminary acceptance substituted for missing worker terminal")
					}
					if _, err = loadContinuation(ctx, db, Request{ContinueTaskID: parent}, nil); !errors.Is(err, ErrAdmission) {
						t.Fatal("unfinished worker history became continuable", err)
					}
				} else if after.State != "failed" || after.Result == nil || after.Result.TaskID != parent || after.Result.Text != "" || len(receipts) != 1 || receipts[0].Reason != "interrupted_delegation" {
					t.Fatal("committed worker was not recovered as interrupted parent")
				}
				for task, before := range journals {
					page, err := db.ReadEventPage(ctx, task, 0, 100)
					if err != nil || page.HasMore {
						t.Fatal(err)
					}
					if task == parent && boundary == "parent_release" {
						if len(page.Events) != len(before)+2 || !reflect.DeepEqual(before, page.Events[:len(before)]) {
							t.Fatal("recovery changed parent prefix or repeated repair")
						}
						tool, end := page.Events[len(before)], page.Events[len(before)+1]
						if tool.Kind != runtime.ToolCompleted || tool.Data.Code != "delegation_recovered" || !strings.Contains(tool.Data.Text, "durable worker candidate") || end.Kind != runtime.TaskFailed || end.CausationID != tool.ID {
							t.Fatal("incorrect recovered finalization checkpoint")
						}
						if attempt == 0 {
							recoveredParent = page.Events
						} else if !reflect.DeepEqual(recoveredParent, page.Events) {
							t.Fatal("repeat recovery rewrote repaired parent events")
						}
					} else if !reflect.DeepEqual(before, page.Events) {
						t.Fatal("recovery rewrote child or unfinished history")
					}
				}
				currentWorker, workerErr := db.InspectLeases(ctx, "delegation-"+parent)
				currentParent, parentErr := db.InspectLeases(ctx, "delegation")
				if workerErr != nil || parentErr != nil || !reflect.DeepEqual(workerLeases, currentWorker) || !reflect.DeepEqual(parentLeases, currentParent) || parents.Load() != 1 || children.Load() != 1 {
					t.Fatal("recovery altered orphan ownership or redispatched inference")
				}
			}
			// Journal repair alone still preserves leases. The separate daemon
			// sweep may now reclaim only the terminal parent's verified orphan.
			if boundary == "parent_release" {
				journals[parent] = recoveredParent
				qualifyTerminalReaderSweep(t, ctx, svc, db, raw, parentLeases[0], journals)
			} else {
				if _, reclaimed, err := db.RecoverTerminalReadersPage(ctx, "", 32, time.Now().UTC()); err != nil || reclaimed != 0 {
					t.Fatal("running worker reader was reclaimed", err, reclaimed)
				}
				qualifyOrphanWorkerSweep(t, ctx, svc, db, raw, id, parentLeases[0], workerLeases[0], journals)
			}
			if parents.Load() != 1 || children.Load() != 1 {
				t.Fatal("reader sweep dispatched inference")
			}
		})
	}
}

func killWorkerFinishFixture(t *testing.T, ctx context.Context, cfg config.Settings, boundary string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	body, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestInterruptedDelegationCrashProcessHelper$")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "DARWIN_INTERRUPTED_CRASH_HELPER=1", "DARWIN_INTERRUPTED_CRASH_CONFIG=" + path, "DARWIN_INTERRUPTED_CRASH_BOUNDARY=" + boundary}
	cmd.WaitDelay, cmd.Stderr = time.Second, io.Discard
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, scanned := make(chan string, 1), make(chan struct{})
	go func() {
		defer close(scanned)
		scanner := bufio.NewScanner(pipe)
		scanner.Buffer(make([]byte, 256), 4096)
		if scanner.Scan() {
			line <- scanner.Text()
		} else {
			line <- ""
		}
	}()
	waited := false
	defer func() {
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		<-scanned
	}()
	var id string
	select {
	case id = <-line:
	case <-ctx.Done():
		t.Fatal("worker fixture did not reach crash boundary")
	}
	if id == "" || len(id) > 128 || strings.ContainsAny(id, " \t\r\n") {
		t.Fatal("invalid crash boundary acknowledgement")
	}
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	waited = true
	if err == nil || cmd.ProcessState == nil {
		t.Fatal("fixture returned rather than being killed")
	}
	if state, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); !ok || !state.Signaled() || state.Signal() != syscall.SIGKILL {
		t.Fatal("fixture was not terminated by SIGKILL")
	}
	return id
}
