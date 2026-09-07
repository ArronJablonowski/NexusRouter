package telemetry

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/submissions"
)

func terminalFixture(t *testing.T, db *Store, claim submissions.Claim, task, retry, mode string) {
	t.Helper()
	kinds := []runtime.Kind{runtime.TaskStarted, runtime.TurnStarted, runtime.TurnCompleted, runtime.EvaluationRecorded, runtime.TaskCompleted}
	if mode != "success" {
		kinds = []runtime.Kind{runtime.TaskStarted, runtime.TurnStarted, runtime.ErrorRecorded, runtime.TaskFailed}
		if mode == "canceled" {
			kinds[3] = runtime.TaskCanceled
		}
	}
	for i, kind := range kinds {
		e := event(fmt.Sprintf("%s-%d", task, i), int64(i+1), kind)
		e.TaskID = task
		e.CorrelationID = task
		e.TurnID = "turn"
		e.AttemptID = "attempt"
		e.Data.ModelID = "model"
		e.Data.ProviderID = "fixture"
		e.Time = time.Now()
		switch kind {
		case runtime.TaskStarted:
			e.TurnID = ""
			e.Data.SubmissionID = claim.Status.ID
			e.Data.RetryOfTaskID = retry
			e.Data.ModelID = "model"
			e.Data.ProviderID = "fixture"
			e.Data.Messages = []providers.Message{{Role: "user", Content: "question"}}
		case runtime.TurnCompleted:
			e.Data.Text = "answer"
			e.Data.FinishReason = "stop"
		case runtime.EvaluationRecorded:
			accepted := true
			e.Data.Accepted = &accepted
			e.Data.Code = "deterministic.nonempty_text.v1"
		case runtime.ErrorRecorded, runtime.TaskFailed:
			e.Data.Code = "provider_retryable_no_output"
		case runtime.TaskCanceled:
			e.Data.Code = "canceled"
		}
		if err := db.AppendSubmission(context.Background(), int64(i), e, claim.Status.ID, claim.Token); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTerminalSubmissionRecovery(t *testing.T) {
	for _, mode := range []string{"success", "failed", "canceled", "fallback", "fallback_chain", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			db, _ := submissionStore(t)
			ctx := context.Background()
			job := queuedSubmission(t, db, "job")
			claim := claimSubmission(t, db)
			fixture := mode
			if mode == "fallback" || mode == "fallback_chain" {
				fixture = "failed"
			}
			if mode == "cancel" {
				fixture = "success"
			}
			terminalFixture(t, db, claim, "task", "", fixture)
			if mode == "fallback" {
				terminalFixture(t, db, claim, "second", "task", "success")
			}
			if mode == "fallback_chain" {
				terminalFixture(t, db, claim, "second", "task", "failed")
				terminalFixture(t, db, claim, "third", "second", "success")
			}
			if mode == "cancel" {
				if _, err := db.CancelSubmission(ctx, job.ID); err != nil {
					t.Fatal(err)
				}
			}
			now := time.Now().Add(2 * time.Minute)
			ok, err := db.RecoverTerminalSubmission(ctx, job.ID, submitDigest("config"), now)
			if err != nil || !ok {
				t.Fatalf("recover %v %v", ok, err)
			}
			status, err := db.Submission(ctx, job.ID)
			if err != nil {
				t.Fatal(err)
			}
			expected := mode
			if mode == "success" || mode == "fallback" || mode == "fallback_chain" {
				expected = "succeeded"
			}
			if mode == "cancel" {
				expected = "canceled"
			}
			if status.State != expected || status.LeaseExpiresAt != nil {
				t.Fatalf("status %+v", status)
			}
			if status.Result != nil {
				if status.Result.AuditStatus != "not_recovered" || status.Result.AuditID != "" {
					t.Fatal(status.Result)
				}
				if expected != "succeeded" && status.Result.Text != "" {
					t.Fatal("partial text exposed")
				}
			}
			if mode == "fallback" && (status.Result.TaskID != "second" || len(status.Result.PreviousTaskIDs) != 1 || status.Result.PreviousTaskIDs[0] != "task") {
				t.Fatal(status.Result)
			}
			if mode == "fallback_chain" && (status.Result.TaskID != "third" || fmt.Sprint(status.Result.PreviousTaskIDs) != "[task second]") {
				t.Fatal(status.Result)
			}
			if ok, err = db.RecoverTerminalSubmission(ctx, job.ID, submitDigest("config"), now); ok || err != nil {
				t.Fatal("not idempotent", ok, err)
			}
			if _, err = db.FinishSubmission(ctx, job.ID, claim.Token, "failed", "interrupted", nil); !errors.Is(err, submissions.ErrLeaseLost) {
				t.Fatalf("old owner %v", err)
			}
			history, err := db.RecoveryHistory(ctx, job.ID)
			if err != nil || len(history) != 1 {
				t.Fatal(history, err)
			}
		})
	}
}

func TestTerminalSubmissionRecoveryRejectsUnsafeHistory(t *testing.T) {
	for _, mode := range []string{"gap", "head", "body", "oversize", "partial", "link", "fallback_output", "continuation"} {
		t.Run(mode, func(t *testing.T) {
			db, _ := submissionStore(t)
			ctx := context.Background()
			job := queuedSubmission(t, db, "job")
			claim := claimSubmission(t, db)
			terminalFixture(t, db, claim, "task", "", "success")
			var query string
			switch mode {
			case "continuation":
				query = `UPDATE events SET body=json_set(body,'$.data.parent_task_id','missing-work') WHERE sequence=1`
			case "gap":
				query = `DELETE FROM events WHERE sequence=2`
			case "head":
				query = `UPDATE task_heads SET state='failed'`
			case "body":
				query = `UPDATE events SET body='{}' WHERE sequence=2`
			case "oversize":
				query = `UPDATE events SET body=json_object('padding',printf('%.*c',8388609,'x')) WHERE sequence=2`
			case "partial":
				query = `UPDATE task_heads SET state='running'`
			case "link":
				terminalFixture(t, db, claim, "second", "wrong", "success")
			case "fallback_output":
				terminalFixture(t, db, claim, "second", "task", "success")
				query = `UPDATE events SET body=json_set(body,'$.kind','task.failed','$.data.code','provider_retryable_no_output') WHERE task_id='task' AND sequence=5; UPDATE task_heads SET state='failed' WHERE task_id='task'`
			}
			if query != "" {
				if _, err := db.db.Exec(query); err != nil {
					t.Fatal(err)
				}
			}
			ok, err := db.RecoverTerminalSubmission(ctx, job.ID, submitDigest("config"), time.Now().Add(2*time.Minute))
			if ok || (mode != "partial" && err == nil) {
				t.Fatalf("unsafe accepted %v %v", ok, err)
			}
			status, _ := db.Submission(ctx, job.ID)
			if status.State != "running" {
				t.Fatal(status)
			}
		})
	}
}

func TestTerminalSubmissionCancelRaceAndRollback(t *testing.T) {
	db, _ := submissionStore(t)
	ctx := context.Background()
	job := queuedSubmission(t, db, "job")
	claim := claimSubmission(t, db)
	terminalFixture(t, db, claim, "task", "", "success")
	if _, err := db.db.Exec(`CREATE TRIGGER reject_terminal_recovery BEFORE UPDATE ON submissions WHEN NEW.token='' BEGIN SELECT RAISE(ABORT,'failure'); END`); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.RecoverTerminalSubmission(ctx, job.ID, submitDigest("config"), time.Now().Add(2*time.Minute)); ok || err == nil {
		t.Fatal("rollback missing")
	}
	history, err := db.RecoveryHistory(ctx, job.ID)
	if err != nil || len(history) != 0 {
		t.Fatal(history, err)
	}
	if _, err = db.db.Exec(`DROP TRIGGER reject_terminal_recovery`); err != nil {
		t.Fatal(err)
	}
	var cancelErr, recoverErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, cancelErr = db.CancelSubmission(ctx, job.ID) }()
	go func() {
		defer wg.Done()
		_, recoverErr = db.RecoverTerminalSubmission(ctx, job.ID, submitDigest("config"), time.Now().Add(2*time.Minute))
	}()
	wg.Wait()
	if cancelErr != nil || recoverErr != nil {
		t.Fatal(cancelErr, recoverErr)
	}
	status, err := db.Submission(ctx, job.ID)
	if err != nil || (status.State != "succeeded" && status.State != "canceled") {
		t.Fatal(status, err)
	}
	if status.State == "canceled" && status.Result != nil && status.Result.Text != "" {
		t.Fatal("canceled text")
	}
}
