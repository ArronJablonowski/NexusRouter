package app

import (
	"bufio"
	"context"
	"database/sql"
	"database/sql/driver"
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
	"modernc.org/sqlite"
)

// Only the test-owned child process registers this SQLite function. A trigger
// stops inside the parent's actual result transaction, before INSERT commits.
// The owner is SIGKILLed there: no Run return, cancellation, deferred database
// closure, or graceful application teardown can manufacture the crash state.
func TestInterruptedDelegationCrashProcessHelper(t *testing.T) {
	if os.Getenv("DARWIN_INTERRUPTED_CRASH_HELPER") != "1" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	body, err := os.ReadFile(os.Getenv("DARWIN_INTERRUPTED_CRASH_CONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg config.Settings
	if json.Unmarshal(body, &cfg) != nil {
		t.Fatal("invalid helper configuration")
	}
	var submissionID string
	if err := sqlite.RegisterScalarFunction("darwin_test_pause_result", 0, func(_ *sqlite.FunctionContext, _ []driver.Value) (driver.Value, error) {
		fmt.Println(submissionID)
		<-ctx.Done()
		return nil, errors.New("parent did not kill fixture at crash boundary")
	}); err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc.profile = delegateProcessProfile
	request := Request{ModelID: "parent", Prompt: "Delegate then review"}
	status, err := svc.Submit(ctx, "abrupt-delegation-result", request)
	if err != nil {
		t.Fatal(err)
	}
	submissionID = status.ID
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	claim, err := db.ClaimSubmission(ctx, svc.submissionConfigDigest(), time.Now().UTC(), time.Minute)
	if err != nil || claim.Status.ID != submissionID {
		t.Fatal("claim failed", err)
	}
	raw, err := sql.Open("sqlite", cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	trigger := `CREATE TRIGGER crash_before_parent_tool_result BEFORE INSERT ON events WHEN json_extract(NEW.body,'$.kind')='tool.completed' AND json_extract(NEW.body,'$.data.tool_name') IN ('delegate','delegate_batch') BEGIN SELECT darwin_test_pause_result(); END`
	switch os.Getenv("DARWIN_INTERRUPTED_CRASH_BOUNDARY") {
	case "":
	case "worker_release":
		trigger = `CREATE TRIGGER crash_before_parent_tool_result BEFORE UPDATE OF released ON resource_leases WHEN OLD.scope GLOB 'delegation-*' AND OLD.writer=0 AND NEW.released=1 BEGIN SELECT darwin_test_pause_result(); END`
	case "parent_release":
		trigger = `CREATE TRIGGER crash_before_parent_tool_result BEFORE UPDATE OF released ON resource_leases WHEN OLD.scope='delegation' AND OLD.writer=0 AND NEW.released=1 BEGIN SELECT darwin_test_pause_result(); END`
	default:
		t.Fatal("unsupported test crash boundary")
	}
	if _, err := raw.ExecContext(ctx, trigger); err != nil {
		t.Fatal(err)
	}
	request.submissionID, request.submissionToken = submissionID, claim.Token
	_, err = svc.Run(ctx, request)
	t.Fatal("helper returned instead of being killed at result commit", err)
}

func TestInterruptedDelegationRecoveredAfterAbruptProcessDeath(t *testing.T) {
	if stdRuntime.GOOS != "darwin" && stdRuntime.GOOS != "linux" {
		t.Skip("SIGKILL qualification requires Unix")
	}
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprint(batch), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			var parents, children atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Model string `json:"model"`
				}
				if json.NewDecoder(r.Body).Decode(&request) != nil {
					t.Error("invalid fixture request")
					return
				}
				switch request.Model {
				case "child":
					children.Add(1)
					fmt.Fprintln(w, `{"message":{"content":"crash-qualified child answer"},"done":true,"done_reason":"stop"}`)
				case "parent":
					if parents.Add(1) != 1 {
						t.Error("coordinator redispatched across crash/recovery")
						w.WriteHeader(500)
						return
					}
					if batch {
						fmt.Fprintln(w, `{"message":{"tool_calls":[{"function":{"name":"delegate_batch","arguments":{"tasks":[{"prompt":"first","validation":"text"},{"prompt":"second","validation":"text"}]}}}]},"done":true,"done_reason":"tool_calls"}`)
					} else {
						fmt.Fprintln(w, `{"message":{"tool_calls":[{"function":{"name":"delegate","arguments":{"prompt":"first","validation":"text"}}}]},"done":true,"done_reason":"tool_calls"}`)
					}
				default:
					t.Error("unexpected model")
					w.WriteHeader(500)
				}
			}))
			defer provider.Close()
			cfg := config.Defaults()
			cfg.Mode, cfg.Workers.Max, cfg.Hardware.Concurrent = "local_only", 3, "3"
			cfg.Workers.DelegateModel, cfg.Workers.DelegateMaxCalls = "child", 2
			cfg.Memory.Enabled, cfg.Skills.Enabled, cfg.Tools.Enabled = false, false, false
			cfg.Telemetry.Database = filepath.Join(t.TempDir(), "crashed-delegation.db")
			cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: provider.URL}}
			zero := 0.0
			for _, id := range []string{"parent", "child"} {
				cfg.Models = append(cfg.Models, config.Model{ID: id, Model: id, Provider: "local", Locality: "local", RAMBytes: 1, ContextTokens: 16384, EstimatedCost: &zero, Capabilities: []string{"chat"}})
			}
			configuration := filepath.Join(t.TempDir(), "config.json")
			body, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(configuration, body, 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestInterruptedDelegationCrashProcessHelper$")
			cmd.Env = []string{"PATH=/usr/bin:/bin", "DARWIN_INTERRUPTED_CRASH_HELPER=1", "DARWIN_INTERRUPTED_CRASH_CONFIG=" + configuration}
			cmd.WaitDelay = time.Second
			cmd.Stderr = io.Discard
			pipe, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
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
				t.Fatal("helper did not reach durable child/result commit boundary")
			}
			if id == "" || len(id) > 128 || strings.ContainsAny(id, " \t\r\n") {
				t.Fatal("missing crash boundary acknowledgement")
			}
			if err := cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			err = cmd.Wait()
			waited = true
			if err == nil || cmd.ProcessState == nil {
				t.Fatal("helper exited gracefully")
			}
			if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
				t.Fatal("helper did not die by SIGKILL")
			}
			// Reopen only after process death. No child connection is retained by
			// this test process, and the interrupted writer transaction must roll back.
			svc, err := NewService(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			status, err := svc.SubmissionStatus(ctx, id)
			if err != nil || status.State != "running" {
				t.Fatal("submission prematurely finalized", status, err)
			}
			wantChildren := int32(1)
			if batch {
				wantChildren = 2
			}
			if parents.Load() != 1 || children.Load() != wantChildren || len(status.TaskIDs) != 1+int(wantChildren)*2 {
				t.Fatal("unexpected pre-crash execution counts", parents.Load(), children.Load(), status.TaskIDs)
			}
			db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			journals := map[string][]runtime.Event{}
			parent := ""
			for _, task := range status.TaskIDs {
				page, err := db.ReadEventPage(ctx, task, 0, 100)
				if err != nil || page.HasMore || len(page.Events) == 0 {
					t.Fatal("invalid crash snapshot", task, err)
				}
				journals[task] = page.Events
				if page.Events[0].Data.ParentTaskID == "" {
					parent = task
					if page.State != "running" || page.Events[len(page.Events)-1].Kind != runtime.ToolStarted {
						t.Fatal("crash missed uncommitted parent result boundary")
					}
				} else if page.State != "completed" {
					t.Fatal("crash occurred before all child tasks committed terminal state", task, page.State)
				}
			}
			if parent == "" {
				t.Fatal("parent history missing")
			}
			raw, err := sql.Open("sqlite", cfg.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			_, err = raw.ExecContext(ctx, `DROP TRIGGER crash_before_parent_tool_result`)
			closeErr := raw.Close()
			if err != nil || closeErr != nil {
				t.Fatal("fixture trigger cleanup failed", err, closeErr)
			}
			expireRecoveryClaim(t, svc, id)
			dispatcher := &Dispatcher{db: db}
			if _, err := dispatcher.recoverPage(ctx, svc.submissionConfigDigest(), ""); err != nil {
				t.Fatal(err)
			}
			after, err := svc.SubmissionStatus(ctx, id)
			if err != nil || after.State != "failed" || after.Result == nil || after.Result.TaskID != parent || after.Result.Text != "" || !reflect.DeepEqual(status.TaskIDs, after.TaskIDs) {
				t.Fatal("crash recovery misreported parent result", after, err)
			}
			for task, before := range journals {
				page, err := db.ReadEventPage(ctx, task, 0, 100)
				if err != nil || page.HasMore {
					t.Fatal(err)
				}
				if task != parent {
					if !reflect.DeepEqual(before, page.Events) {
						t.Fatal("recovery rewrote child evidence", task)
					}
					continue
				}
				if len(page.Events) != len(before)+2 || !reflect.DeepEqual(before, page.Events[:len(before)]) {
					t.Fatal("recovery rewrote parent prefix")
				}
				tool, end := page.Events[len(before)], page.Events[len(before)+1]
				if tool.Kind != runtime.ToolCompleted || tool.Data.Code != "delegation_recovered" || tool.Data.Effect != runtime.NoEffect || !strings.Contains(tool.Data.Text, "crash-qualified child answer") || end.Kind != runtime.TaskFailed || end.Data.Code != "interrupted_after_delegation" || end.CausationID != tool.ID {
					t.Fatal("incorrect recovered crash checkpoint")
				}
				journals[task] = page.Events
			}
			receipts, err := db.RecoveryHistory(ctx, id)
			if err != nil || len(receipts) != 1 || receipts[0].Reason != "interrupted_delegation" || receipts[0].Action != "failed" {
				t.Fatal("missing recovery receipt", receipts, err)
			}
			if _, err := dispatcher.recoverPage(ctx, svc.submissionConfigDigest(), ""); err != nil {
				t.Fatal(err)
			}
			again, err := db.RecoveryHistory(ctx, id)
			if err != nil || !reflect.DeepEqual(receipts, again) {
				t.Fatal("crash recovery receipt duplicated", err)
			}
			for task, before := range journals {
				page, err := db.ReadEventPage(ctx, task, 0, 100)
				if err != nil || page.HasMore || !reflect.DeepEqual(before, page.Events) {
					t.Fatal("repeat recovery changed journal", task, err)
				}
			}
			if parents.Load() != 1 || children.Load() != wantChildren {
				t.Fatal("recovery redispatched inference")
			}
		})
	}
}
