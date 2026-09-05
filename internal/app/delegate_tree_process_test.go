package app

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/resources"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func delegateProcessProfile(context.Context) (resources.Snapshot, error) {
	return resources.Snapshot{Time: time.Now(), CPUs: 4, TotalRAM: 100000, AvailableRAM: 100000}, nil
}

func TestDelegateTreeProcessHelper(t *testing.T) {
	if os.Getenv("DARWIN_DELEGATE_TREE_HELPER") != "1" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	body, err := os.ReadFile(os.Getenv("DARWIN_DELEGATE_TREE_CONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg config.Settings
	if json.Unmarshal(body, &cfg) != nil {
		t.Fatal("invalid fixture config")
	}
	svc, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc.profile = delegateProcessProfile
	r := Request{ModelID: "chat", Prompt: "delegate durable work"}
	status, err := svc.Submit(ctx, "process-tree-idempotency", r)
	if err != nil {
		t.Fatal(err)
	}
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	claim, err := db.ClaimSubmission(ctx, svc.submissionConfigDigest(), time.Now().UTC(), 5*time.Second)
	if err != nil || claim.Status.ID != status.ID {
		t.Fatal("claim failed", err)
	}
	r.submissionID, r.submissionToken = status.ID, claim.Token
	out, err := svc.Run(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := db.TaskSnapshot(ctx, out.TaskID)
	if err != nil || snapshot.State != "completed" {
		t.Fatal("parent not durable", err)
	}
	fmt.Println(status.ID)
	<-ctx.Done()
}

func TestDelegateTreeRecoveredAfterOwnerProcessKilled(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprint(batch), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			var calls, parents atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var body struct {
					Model string `json:"model"`
				}
				if json.NewDecoder(r.Body).Decode(&body) != nil {
					t.Error("bad body")
					return
				}
				if body.Model == "child" {
					fmt.Fprintln(w, `{"message":{"content":"child answer"},"done":true,"done_reason":"stop"}`)
					return
				}
				if parents.Add(1) == 1 {
					if batch {
						fmt.Fprintln(w, `{"message":{"tool_calls":[{"function":{"name":"delegate_batch","arguments":{"tasks":[{"prompt":"one","validation":"text"},{"prompt":"two","validation":"text"}]}}}]},"done":true,"done_reason":"tool_calls"}`)
					} else {
						fmt.Fprintln(w, `{"message":{"tool_calls":[{"function":{"name":"delegate","arguments":{"prompt":"one","validation":"text"}}}]},"done":true,"done_reason":"tool_calls"}`)
					}
					return
				}
				fmt.Fprintln(w, `{"message":{"content":"durable parent answer"},"done":true,"done_reason":"stop"}`)
			}))
			defer provider.Close()
			cfg := config.Defaults()
			cfg.Mode = "local_only"
			cfg.Workers.Max = 3
			cfg.Hardware.Concurrent = "3"
			cfg.Workers.DelegateModel = "child"
			cfg.Workers.DelegateMaxCalls = 2
			cfg.Telemetry.Database = filepath.Join(t.TempDir(), "tree.db")
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
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDelegateTreeProcessHelper$")
			cmd.Env = append(os.Environ(), "DARWIN_DELEGATE_TREE_HELPER=1", "DARWIN_DELEGATE_TREE_CONFIG="+path)
			cmd.WaitDelay = time.Second
			pipe, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			line, scanDone := make(chan string, 1), make(chan struct{})
			go func() {
				defer close(scanDone)
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
				<-scanDone
			}()
			var id string
			select {
			case id = <-line:
			case <-ctx.Done():
				t.Fatal("helper did not acknowledge durable tree")
			}
			if id == "" || strings.ContainsAny(id, " \t\r\n") {
				t.Fatal("invalid helper acknowledgment")
			}
			if err := cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			err = cmd.Wait()
			waited = true
			if err == nil || cmd.ProcessState == nil || cmd.ProcessState.Success() {
				t.Fatal("helper was not killed", err)
			}
			if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
				t.Fatal("helper did not terminate by SIGKILL", cmd.ProcessState)
			}
			svc, err := NewService(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			svc.profile = delegateProcessProfile
			before, err := svc.SubmissionStatus(ctx, id)
			if err != nil || before.State != "running" {
				t.Fatal(before, err)
			}
			wantTasks, wantCalls := 3, int32(3)
			if batch {
				wantTasks, wantCalls = 5, 4
			}
			if len(before.TaskIDs) != wantTasks || calls.Load() != wantCalls {
				t.Fatal(before.TaskIDs, calls.Load())
			}
			db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			journals := map[string][]runtime.Event{}
			parent := ""
			for _, task := range before.TaskIDs {
				snapshot, err := db.TaskSnapshot(ctx, task)
				if err != nil || snapshot.State != "completed" {
					t.Fatal(snapshot, err)
				}
				if snapshot.ParentTaskID == "" {
					parent = task
				}
				events, err := db.Read(ctx, task, 0, 100)
				if err != nil {
					t.Fatal(err)
				}
				journals[task] = events
			}
			dispatcher, err := StartDispatcher(ctx, svc)
			if err != nil {
				t.Fatal(err)
			}
			defer dispatcher.Close()
			after := awaitSubmission(t, ctx, svc, id, "succeeded")
			if err := dispatcher.Close(); err != nil {
				t.Fatal(err)
			}
			if after.Result == nil || after.Result.TaskID != parent || after.Result.Text != "durable parent answer" || !reflect.DeepEqual(before.TaskIDs, after.TaskIDs) || calls.Load() != wantCalls {
				t.Fatal(after, calls.Load())
			}
			for task, before := range journals {
				after, err := db.Read(ctx, task, 0, 100)
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatal("journal mutated", task, err)
				}
			}
			history, err := svc.SubmissionRecoveries(ctx, id)
			if err != nil || len(history) != 1 || history[0].Reason != "terminal_history" || history[0].Action != "succeeded" {
				t.Fatal(history, err)
			}
		})
	}
}
