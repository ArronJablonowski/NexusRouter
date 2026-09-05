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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/resources"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

// Inject failure at the actual parent's result-commit boundary, after real
// runtime/supervisor execution against HTTP fixtures. Recovery must only write
// missing records; a separate explicit continuation consumes the result.
func TestInterruptedDelegationRestoresResultAndExplicitlyContinues(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprint(batch), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			var parentCalls, childCalls atomic.Int32
			var sawRecoveredResult atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Model    string              `json:"model"`
					Messages []providers.Message `json:"messages"`
				}
				if json.NewDecoder(r.Body).Decode(&body) != nil {
					t.Error("bad request")
					return
				}
				if body.Model == "child" {
					childCalls.Add(1)
					fmt.Fprintln(w, `{"message":{"content":"durable child answer"},"done":true,"done_reason":"stop"}`)
					return
				}
				if body.Model != "parent" {
					t.Error("unknown model")
					return
				}
				if parentCalls.Add(1) == 1 {
					if batch {
						fmt.Fprintln(w, `{"message":{"tool_calls":[{"function":{"name":"delegate_batch","arguments":{"tasks":[{"prompt":"first","validation":"text"},{"prompt":"second","validation":"text"}]}}}]},"done":true,"done_reason":"tool_calls"}`)
					} else {
						fmt.Fprintln(w, `{"message":{"tool_calls":[{"function":{"name":"delegate","arguments":{"prompt":"first","validation":"text"}}}]},"done":true,"done_reason":"tool_calls"}`)
					}
					return
				}
				for _, m := range body.Messages {
					if m.Role == "tool" && strings.Contains(m.Content, "durable child answer") {
						sawRecoveredResult.Store(true)
					}
				}
				fmt.Fprintln(w, `{"message":{"content":"reviewed recovered child answer"},"done":true,"done_reason":"stop"}`)
			}))
			defer server.Close()
			cfg := config.Defaults()
			cfg.Mode, cfg.Workers.Max, cfg.Hardware.Concurrent = "local_only", 3, "3"
			cfg.Workers.DelegateModel, cfg.Workers.DelegateMaxCalls = "child", 2
			cfg.Telemetry.Database = filepath.Join(t.TempDir(), "interrupted.db")
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
			request := Request{ModelID: "parent", Prompt: "delegate and review"}
			status, err := svc.Submit(ctx, "interrupted-delegation-key", request)
			if err != nil {
				t.Fatal(err)
			}
			db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			claim, err := db.ClaimSubmission(ctx, svc.submissionConfigDigest(), time.Now().UTC(), time.Minute)
			if err != nil || claim.Status.ID != status.ID {
				t.Fatal(err)
			}
			raw, err := sql.Open("sqlite", cfg.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			if _, err := raw.Exec(`CREATE TRIGGER interrupt_parent_result BEFORE INSERT ON events WHEN json_extract(NEW.body,'$.kind')='tool.completed' AND json_extract(NEW.body,'$.data.tool_name') IN ('delegate','delegate_batch') BEGIN SELECT RAISE(ABORT,'fixture interrupted commit'); END`); err != nil {
				t.Fatal(err)
			}
			request.submissionID, request.submissionToken = status.ID, claim.Token
			result, runErr := svc.Run(ctx, request)
			if runErr == nil || result.TaskID == "" {
				t.Fatal("fault boundary not reached", result, runErr)
			}
			if _, err := raw.Exec(`DROP TRIGGER interrupt_parent_result`); err != nil {
				t.Fatal(err)
			}
			before, err := db.ReadEventPage(ctx, result.TaskID, 0, 100)
			if err != nil || before.HasMore || before.State != "running" || before.Events[len(before.Events)-1].Kind != runtime.ToolStarted {
				t.Fatal("not interrupted awaiting tool result", before.State, err)
			}
			readiness, err := InspectTaskContinuation(ctx, cfg.Telemetry.Database, result.TaskID)
			if err != nil || readiness.HistoryEligible || readiness.Reason != "pending_tools" || readiness.Sequence != before.HeadSequence {
				t.Fatal("unfinished history reported ready", readiness, err)
			}
			status, err = svc.SubmissionStatus(ctx, status.ID)
			if err != nil {
				t.Fatal(err)
			}
			children := map[string][]runtime.Event{}
			for _, id := range status.TaskIDs {
				if id == result.TaskID {
					continue
				}
				page, err := db.ReadEventPage(ctx, id, 0, 100)
				if err != nil || page.State != "completed" || page.HasMore {
					t.Fatal("child not fully terminal", id, err)
				}
				children[id] = page.Events
			}
			wantChildren := int32(1)
			if batch {
				wantChildren = 2
			}
			if childCalls.Load() != wantChildren || parentCalls.Load() != 1 || len(children) != int(wantChildren)*2 {
				t.Fatal("unexpected initial calls", childCalls.Load(), parentCalls.Load())
			}
			// Exercise the planner independently of storage before the restart
			// transaction, preserving the task-start order from submission status.
			var histories [][]runtime.Event
			for _, id := range status.TaskIDs {
				if id == result.TaskID {
					histories = append(histories, before.Events)
				} else {
					histories = append(histories, children[id])
				}
			}
			if _, err := sessions.PlanInterruptedDelegation(histories, time.Now().UTC(), false); err != nil {
				t.Fatalf("runtime history not planned: %v", err)
			}
			expireRecoveryClaim(t, svc, status.ID)
			d := &Dispatcher{db: db}
			if _, err := d.recoverPage(ctx, svc.submissionConfigDigest(), ""); err != nil {
				t.Fatal(err)
			}
			status, err = svc.SubmissionStatus(ctx, status.ID)
			if err != nil || status.State != "failed" || status.Result == nil || status.Result.TaskID != result.TaskID || status.Result.Text != "" {
				t.Fatal("interruption not visibly closed", status, err)
			}
			after, err := db.ReadEventPage(ctx, result.TaskID, 0, 100)
			if err != nil || after.HasMore || after.HeadSequence != before.HeadSequence+2 || !reflect.DeepEqual(before.Events, after.Events[:len(before.Events)]) {
				t.Fatal("unexpected recovery history rewrite", err)
			}
			tool, terminal := after.Events[len(after.Events)-2], after.Events[len(after.Events)-1]
			if tool.Kind != runtime.ToolCompleted || tool.Data.Effect != runtime.NoEffect || tool.Data.Code != "delegation_recovered" || !strings.Contains(tool.Data.Text, "durable child answer") || terminal.Kind != runtime.TaskFailed || terminal.Data.Code != "interrupted_after_delegation" || terminal.CausationID != tool.ID {
				t.Fatal("incorrect recovery checkpoint", tool, terminal)
			}
			readiness, err = InspectTaskContinuation(ctx, cfg.Telemetry.Database, result.TaskID)
			if err != nil || !readiness.HistoryEligible || readiness.Reason != "recovered_delegation" || readiness.Sequence != after.HeadSequence {
				t.Fatal("restored history not reported ready", readiness, err)
			}
			for id, original := range children {
				page, err := db.ReadEventPage(ctx, id, 0, 100)
				if err != nil || !reflect.DeepEqual(original, page.Events) {
					t.Fatal("child history changed", id, err)
				}
			}
			if parentCalls.Load() != 1 || childCalls.Load() != wantChildren {
				t.Fatal("recovery executed a provider")
			}
			history, err := db.RecoveryHistory(ctx, status.ID)
			if err != nil || len(history) != 1 {
				t.Fatal("missing recovery receipt", err)
			}
			if _, err := d.recoverPage(ctx, svc.submissionConfigDigest(), ""); err != nil {
				t.Fatal(err)
			}
			again, err := db.ReadEventPage(ctx, result.TaskID, 0, 100)
			if err != nil || !reflect.DeepEqual(after, again) {
				t.Fatal("recovery was not idempotent", err)
			}
			fresh, err := NewService(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			fresh.profile = svc.profile
			continued, err := fresh.Run(ctx, Request{ModelID: "parent", ContinueTaskID: result.TaskID, Prompt: "Review the recovered result without delegating again."})
			if err != nil || continued.Text != "reviewed recovered child answer" || !sawRecoveredResult.Load() || parentCalls.Load() != 2 || childCalls.Load() != wantChildren {
				t.Fatal("explicit continuation lost result or repeated work", continued, err, parentCalls.Load(), childCalls.Load())
			}
		})
	}
}

func TestRecoveredContinuationRequiresExactCheckpoint(t *testing.T) {
	// This helper only checks the final checkpoint. loadContinuation also
	// requires failed state, complete tool pairing and no uncertain effects.
	for _, mode := range []string{"valid", "ordinary failure", "wrong cause", "wrong turn", "wrong attempt", "uncertain", "canceled"} {
		tool := runtime.Event{Version: 1, ID: "tool-end", TaskID: "task", SessionID: "session", Sequence: 8, Kind: runtime.ToolCompleted, TurnID: "turn", AttemptID: "attempt", Data: runtime.Data{ToolName: "delegate", Effect: runtime.NoEffect, Code: "delegation_recovered"}}
		end := runtime.Event{Version: 1, ID: "end", TaskID: "task", SessionID: "session", Sequence: 9, Kind: runtime.TaskFailed, TurnID: "turn", AttemptID: "attempt", CausationID: tool.ID, Data: runtime.Data{Code: "interrupted_after_delegation"}}
		switch mode {
		case "ordinary failure":
			end.Data.Code = "execution_failed"
		case "wrong cause":
			end.CausationID = "other"
		case "wrong turn":
			end.TurnID = "other"
		case "wrong attempt":
			end.AttemptID = "other"
		case "uncertain":
			tool.Data.Effect = runtime.UncertainEffect
		case "canceled":
			end.Kind = runtime.TaskCanceled
		}
		reader := recoveredCheckpointReader{tool, end}
		ok := recoveredDelegationContinuation(context.Background(), reader, sessions.Snapshot{TaskID: "task", SessionID: "session", State: "failed", Sequence: 9})
		if ok != (mode == "valid") {
			t.Fatal("checkpoint admission", mode, ok)
		}
	}
}

type recoveredCheckpointReader []runtime.Event

func (r recoveredCheckpointReader) Read(_ context.Context, task string, after int64, limit int) ([]runtime.Event, error) {
	return r, nil
}
