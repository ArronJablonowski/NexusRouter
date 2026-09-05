package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/submissions"
)

func interruptedModelFixture(t *testing.T, db *Store, claim submissions.Claim, parent string) []runtime.Event {
	t.Helper()
	events := []runtime.Event{}
	for i, kind := range []runtime.Kind{runtime.TaskStarted, runtime.TurnStarted, runtime.ModelDelta} {
		e := runtime.Event{Version: 1, ID: fmt.Sprintf("model-event-%d", i), TaskID: "model-task", SessionID: "model-session", CorrelationID: "model-task", Sequence: int64(i + 1), Time: time.Now().UTC().Add(-time.Second), Kind: kind}
		if i == 0 {
			e.Data = runtime.Data{SubmissionID: claim.Status.ID, ParentTaskID: parent, Privacy: "cloud_allowed", Messages: []providers.Message{{Role: "user", Content: "PRIVATE_SOURCE_PROMPT"}}}
		} else {
			e.TurnID, e.AttemptID = "turn", "attempt"
			e.Data.ProviderID, e.Data.ModelID = "fixture", "model"
		}
		if kind == runtime.ModelDelta {
			e.Data.Text = "PRIVATE_PARTIAL_OUTPUT"
		}
		if err := db.AppendSubmission(context.Background(), int64(i), e, claim.Status.ID, claim.Token); err != nil {
			t.Fatal(err)
		}
		events = append(events, e)
	}
	return events
}

func TestInterruptedModelRecoveryAtomicAndFenced(t *testing.T) {
	for _, mode := range []string{"failed", "submission_cancel", "task_cancel", "continuation"} {
		t.Run(mode, func(t *testing.T) {
			db, _ := submissionStore(t)
			ctx := context.Background()
			job := queuedSubmission(t, db, "model-job")
			claim := claimSubmission(t, db)
			parent := ""
			if mode == "continuation" {
				parent = "prior-task"
				if _, err := db.db.Exec(`UPDATE submissions SET request=json_set(request,'$.request.ContinueTaskID','prior-task')`); err != nil {
					t.Fatal(err)
				}
			}
			before := interruptedModelFixture(t, db, claim, parent)
			if mode == "submission_cancel" {
				if _, err := db.CancelSubmission(ctx, job.ID); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "task_cancel" {
				if _, err := db.RequestCancellation(ctx, "model-task"); err != nil {
					t.Fatal(err)
				}
			}
			now := time.Now().Add(2 * time.Minute)
			ok, err := db.RecoverInterruptedModel(ctx, job.ID, submitDigest("config"), now)
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
			want := "failed"
			if strings.Contains(mode, "cancel") {
				want = "canceled"
			}
			status, err := db.Submission(ctx, job.ID)
			if err != nil || status.State != want || status.LeaseExpiresAt != nil || status.Result == nil || status.Result.TaskID != "model-task" || status.Result.Text != "" {
				t.Fatal(status, err)
			}
			page, err := db.ReadEventPage(ctx, "model-task", 0, 10)
			if err != nil || page.HasMore || len(page.Events) != 4 || page.State != want || !reflect.DeepEqual(before, page.Events[:3]) {
				t.Fatal(page, err)
			}
			receipts, err := db.RecoveryHistory(ctx, job.ID)
			if err != nil || len(receipts) != 1 || receipts[0].Reason != "interrupted_model" || receipts[0].Action != want {
				t.Fatal(receipts, err)
			}
			body, _ := json.Marshal([]any{status.Result, receipts, page.Events[3]})
			if strings.Contains(string(body), "PRIVATE_") {
				t.Fatal("recovery copied private source payload")
			}
			if ok, err = db.RecoverInterruptedModel(ctx, job.ID, submitDigest("config"), now); ok || err != nil {
				t.Fatal(ok, err)
			}
			again, err := db.ReadEventPage(ctx, "model-task", 0, 10)
			if err != nil || !reflect.DeepEqual(page, again) {
				t.Fatal("repeat changed history", err)
			}
			if _, err = db.FinishSubmission(ctx, job.ID, claim.Token, "failed", "execution_failed", nil); !errors.Is(err, submissions.ErrLeaseLost) {
				t.Fatal(err)
			}
			stale := page.Events[3]
			stale.ID, stale.Sequence = "stale-model-owner", 5
			if err := db.AppendSubmission(ctx, 4, stale, job.ID, claim.Token); err == nil {
				t.Fatal("stale owner appended")
			}
		})
	}
}

func TestInterruptedModelRecoveryGuardsAndRollback(t *testing.T) {
	for _, mode := range []string{"live", "config", "continuation", "extra_task", "pending_tool", "raw_bound", "event_failure", "head_failure", "receipt_failure", "submission_failure"} {
		t.Run(mode, func(t *testing.T) {
			db, _ := submissionStore(t)
			ctx := context.Background()
			job := queuedSubmission(t, db, "model-job")
			claim := claimSubmission(t, db)
			interruptedModelFixture(t, db, claim, "")
			now, cfg := time.Now().Add(2*time.Minute), submitDigest("config")
			query := ""
			switch mode {
			case "live":
				now = time.Now()
			case "config":
				cfg = submitDigest("other")
			case "continuation":
				query = `UPDATE submissions SET request=json_set(request,'$.request.ContinueTaskID','other')`
			case "raw_bound":
				query = `UPDATE events SET body=json_set(body,'$.padding',printf('%.*c',8388609,'x')) WHERE sequence=1`
			case "event_failure":
				query = `CREATE TRIGGER reject_model_event BEFORE INSERT ON events BEGIN SELECT RAISE(ABORT,'fixture'); END`
			case "head_failure":
				query = `CREATE TRIGGER reject_model_head BEFORE UPDATE ON task_heads BEGIN SELECT RAISE(ABORT,'fixture'); END`
			case "receipt_failure":
				query = `CREATE TRIGGER reject_model_receipt BEFORE INSERT ON submission_recoveries BEGIN SELECT RAISE(ABORT,'fixture'); END`
			case "submission_failure":
				query = `CREATE TRIGGER reject_model_submission BEFORE UPDATE ON submissions WHEN NEW.state!='running' BEGIN SELECT RAISE(ABORT,'fixture'); END`
			case "extra_task":
				e := runtime.Event{Version: 1, ID: "extra-start", TaskID: "extra", SessionID: "extra", CorrelationID: "extra", Sequence: 1, Time: time.Now(), Kind: runtime.TaskStarted, Data: runtime.Data{SubmissionID: job.ID}}
				if err := db.AppendSubmission(ctx, 0, e, job.ID, claim.Token); err != nil {
					t.Fatal(err)
				}
			case "pending_tool":
				e := runtime.Event{Version: 1, ID: "proposal", TaskID: "model-task", SessionID: "model-session", CorrelationID: "model-task", Sequence: 4, Time: time.Now(), Kind: runtime.TurnCompleted, TurnID: "turn", AttemptID: "attempt", Data: runtime.Data{ProviderID: "fixture", ModelID: "model", FinishReason: "tool_calls", ToolCalls: []providers.ToolCall{{ID: "call", Name: "delegate", Arguments: json.RawMessage(`{}`)}}}}
				if err := db.AppendSubmission(ctx, 3, e, job.ID, claim.Token); err != nil {
					t.Fatal(err)
				}
			}
			if query != "" {
				if _, err := db.db.Exec(query); err != nil {
					t.Fatal(err)
				}
			}
			// Capture raw bytes too: rollback must not rewrite unknown fields.
			var before string
			var headBefore int64
			if err := db.db.QueryRow(`SELECT sequence FROM task_heads WHERE task_id='model-task'`).Scan(&headBefore); err != nil {
				t.Fatal(err)
			}
			if err := db.db.QueryRow(`SELECT group_concat(body,'') FROM (SELECT body FROM events ORDER BY rowid)`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			ok, err := db.RecoverInterruptedModel(ctx, job.ID, cfg, now)
			if ok {
				t.Fatal("unsafe recovery", mode)
			}
			if strings.HasSuffix(mode, "failure") && err == nil {
				t.Fatal("injected transaction failure ignored")
			}
			var after string
			if err := db.db.QueryRow(`SELECT group_concat(body,'') FROM (SELECT body FROM events ORDER BY rowid)`).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if before != after {
				t.Fatal("failed recovery changed journal")
			}
			var headAfter int64
			var headState, token string
			if err := db.db.QueryRow(`SELECT sequence,state FROM task_heads WHERE task_id='model-task'`).Scan(&headAfter, &headState); err != nil || headAfter != headBefore || headState != "running" {
				t.Fatal("failed recovery changed head", err)
			}
			if err := db.db.QueryRow(`SELECT token FROM submissions WHERE id=?`, job.ID).Scan(&token); err != nil || token != claim.Token {
				t.Fatal("failed recovery changed claim", err)
			}
			status, err := db.Submission(ctx, job.ID)
			if err != nil || status.State != "running" || status.Result != nil {
				t.Fatal(status, err)
			}
			receipts, err := db.RecoveryHistory(ctx, job.ID)
			if err != nil || len(receipts) != 0 {
				t.Fatal(receipts, err)
			}
		})
	}
}

func TestInterruptedModelConcurrentRecoveryOneReceipt(t *testing.T) {
	db, _ := submissionStore(t)
	job := queuedSubmission(t, db, "model-job")
	interruptedModelFixture(t, db, claimSubmission(t, db), "")
	var wg sync.WaitGroup
	results := make(chan bool, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := db.RecoverInterruptedModel(context.Background(), job.ID, submitDigest("config"), time.Now().Add(2*time.Minute))
			if err != nil {
				t.Error(err)
			}
			results <- ok
		}()
	}
	wg.Wait()
	close(results)
	count := 0
	for ok := range results {
		if ok {
			count++
		}
	}
	receipts, err := db.RecoveryHistory(context.Background(), job.ID)
	if count != 1 || err != nil || len(receipts) != 1 {
		t.Fatal(count, receipts, err)
	}
}
