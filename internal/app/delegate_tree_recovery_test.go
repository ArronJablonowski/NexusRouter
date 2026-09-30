package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/resources"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func TestDelegateTreeRecoveryDoesNotRepeatExecution(t *testing.T) {
	for _, mode := range []string{"single", "batch", "incomplete_work", "corrupt_worker_output"} {
		t.Run(mode, func(t *testing.T) {
			batch := mode == "batch"
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var calls, parents atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var body struct {
					Model string `json:"model"`
				}
				if json.NewDecoder(r.Body).Decode(&body) != nil {
					t.Error("bad provider body")
					return
				}
				if body.Model == "child" {
					fmt.Fprintln(w, `{"message":{"content":"child answer"},"done":true,"done_reason":"stop"}`)
					return
				}
				if body.Model != "parent" {
					t.Error("unexpected model")
					return
				}
				if parents.Add(1) == 1 {
					if batch {
						fmt.Fprintln(w, `{"message":{"tool_calls":[{"function":{"name":"delegate_batch","arguments":{"tasks":[{"prompt":"first","validation":"text"},{"prompt":"second","validation":"text"}]}}}]},"done":true,"done_reason":"tool_calls"}`)
					} else {
						fmt.Fprintln(w, `{"message":{"tool_calls":[{"function":{"name":"delegate","arguments":{"prompt":"child task","validation":"text"}}}]},"done":true,"done_reason":"tool_calls"}`)
					}
					return
				}
				fmt.Fprintln(w, `{"message":{"content":"parent answer"},"done":true,"done_reason":"stop"}`)
			}))
			defer func() { cancel(); server.Close() }()
			cfg := config.Defaults()
			cfg.Mode = "local_only"
			cfg.Workers.Max = 3
			cfg.Hardware.Concurrent = "3"
			cfg.Workers.DelegateModel = "child"
			cfg.Workers.DelegateMaxCalls = 2
			cfg.Telemetry.Database = filepath.Join(t.TempDir(), "tree.db")
			cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: server.URL}}
			zero := 0.0
			for _, id := range []string{"parent", "child"} {
				cfg.Models = append(cfg.Models, config.Model{ID: id, Model: id, Provider: "local", Locality: "local", RAMBytes: 1, ContextTokens: 16384, EstimatedCost: &zero, Capabilities: []string{"chat"}})
			}
			svc, err := NewService(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			svc.profile = func(context.Context) (resources.Snapshot, error) {
				return resources.Snapshot{Time: time.Now(), CPUs: 4, TotalRAM: 1000, AvailableRAM: 1000}, nil
			}
			request := Request{ModelID: "parent", Prompt: "delegate safely"}
			status, err := svc.Submit(ctx, "tree-recovery-idempotency", request)
			if err != nil {
				t.Fatal(err)
			}
			db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			claim, err := db.ClaimSubmission(ctx, svc.submissionConfigDigest(), time.Now().UTC(), 30*time.Second)
			if err != nil || claim.Status.ID != status.ID {
				t.Fatal(claim.Status, err)
			}
			request.submissionID, request.submissionToken = status.ID, claim.Token
			result, err := svc.Run(ctx, request)
			if err != nil || result.Text != "parent answer" {
				t.Fatal(result, err)
			}
			status, err = svc.SubmissionStatus(ctx, status.ID)
			if err != nil || status.State != "running" || status.Result != nil {
				t.Fatal("ledger was not left unfinished", status, err)
			}
			wantTasks, wantCalls := 3, int32(3)
			if batch {
				wantTasks, wantCalls = 5, 4
			}
			if len(status.TaskIDs) != wantTasks || calls.Load() != wantCalls {
				t.Fatal(status.TaskIDs, calls.Load())
			}
			ids := append([]string(nil), status.TaskIDs...)
			before := map[string][]runtime.Event{}
			for _, id := range ids {
				events, err := db.Read(ctx, id, 0, 100)
				if err != nil {
					t.Fatal(err)
				}
				before[id] = events
			}
			expireRecoveryClaim(t, svc, status.ID)
			if mode == "incomplete_work" || mode == "corrupt_worker_output" {
				workID := ""
				for id, events := range before {
					for _, event := range events {
						if event.Kind == runtime.WorkerCompleted {
							workID = id
						}
					}
				}
				if workID == "" {
					t.Fatal("worker fixture missing")
				}
				raw, err := sql.Open("sqlite", cfg.Telemetry.Database)
				if err != nil {
					t.Fatal(err)
				}
				query := `UPDATE task_heads SET state='running' WHERE task_id=?`
				if mode == "corrupt_worker_output" {
					query = `UPDATE events SET body=json_set(body,'$.data.text','forged worker output') WHERE task_id=? AND json_extract(body,'$.kind')='worker.completed'`
				}
				changedRows, err := raw.ExecContext(ctx, query, workID)
				raw.Close()
				if err != nil {
					t.Fatal(err)
				}
				if count, err := changedRows.RowsAffected(); err != nil || count != 1 {
					t.Fatal(count, err)
				}
				untouched, err := svc.SubmissionStatus(ctx, status.ID)
				if err != nil || untouched.State != "running" {
					t.Fatal(untouched, err)
				}
				changed, _ := db.RecoverTerminalSubmission(ctx, status.ID, svc.submissionConfigDigest(), time.Now().UTC())
				if changed {
					t.Fatal("unsafe tree recovered")
				}
				after, err := svc.SubmissionStatus(ctx, status.ID)
				if err != nil || !reflect.DeepEqual(after, untouched) || calls.Load() != wantCalls {
					t.Fatal("recovery changed ledger or executed inference", after, err, calls.Load())
				}
				history, err := db.RecoveryHistory(ctx, status.ID)
				if err != nil || len(history) != 0 {
					t.Fatal("unsafe tree recorded recovery", history, err)
				}
				return
			}
			restarted, err := NewService(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			dispatcher, err := StartDispatcher(ctx, restarted)
			if err != nil {
				t.Fatal(err)
			}
			defer dispatcher.Close()
			recovered := awaitSubmission(t, ctx, restarted, status.ID, "succeeded")
			if err := dispatcher.Close(); err != nil {
				t.Fatal(err)
			}
			if recovered.Result == nil || recovered.Result.TaskID != result.TaskID || recovered.Result.Text != result.Text || !reflect.DeepEqual(recovered.TaskIDs, ids) || calls.Load() != wantCalls {
				t.Fatal("recovery repeated or lost execution", recovered, calls.Load())
			}
			for _, id := range ids {
				after, err := db.Read(ctx, id, 0, 100)
				if err != nil || !reflect.DeepEqual(before[id], after) {
					t.Fatal("recovery mutated logs", id, err)
				}
			}
			history, err := db.RecoveryHistory(ctx, status.ID)
			if err != nil || len(history) != 1 || history[0].Reason != "terminal_history" {
				t.Fatal(history, err)
			}
			changed, err := db.RecoverTerminalSubmission(ctx, status.ID, svc.submissionConfigDigest(), time.Now().UTC())
			if err != nil || changed {
				t.Fatal("non-idempotent recovery", changed, err)
			}
			again, err := db.RecoveryHistory(ctx, status.ID)
			if err != nil || !reflect.DeepEqual(history, again) || calls.Load() != wantCalls {
				t.Fatal(again, err, calls.Load())
			}
		})
	}
}
