package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/submissions"
)

func TestSubmissionRecoveryLimitHistoryAndFencing(t *testing.T) {
	db, path := submissionStore(t)
	ctx := context.Background()
	job := queuedSubmission(t, db, "job")
	for i := 0; i < 4; i++ {
		claim := claimSubmission(t, db)
		now := time.Now().Add(2 * time.Minute)
		ok, err := db.RecoverUndispatched(ctx, job.ID, submitDigest("config"), now)
		if err != nil || !ok {
			t.Fatalf("recover %d %v %v", i, ok, err)
		}
		ok, err = db.RecoverUndispatched(ctx, job.ID, submitDigest("config"), now)
		if err != nil || ok {
			t.Fatalf("repeat %v %v", ok, err)
		}
		if _, err = db.RenewSubmission(ctx, job.ID, claim.Token, now, time.Minute); !errors.Is(err, submissions.ErrLeaseLost) {
			t.Fatalf("old renewal %v", err)
		}
		if _, err = db.FinishSubmission(ctx, job.ID, claim.Token, "failed", "interrupted", nil); !errors.Is(err, submissions.ErrLeaseLost) {
			t.Fatalf("old finish %v", err)
		}
		start := event(fmt.Sprintf("start-%d", i), 1, runtime.TaskStarted)
		start.Data.SubmissionID = job.ID
		if err = db.AppendSubmission(ctx, 0, start, job.ID, claim.Token); !errors.Is(err, runtime.ErrExecutionLeaseLost) {
			t.Fatalf("old append %v", err)
		}
		status, err := db.Submission(ctx, job.ID)
		if err != nil || status.LeaseExpiresAt != nil || len(status.TaskIDs) != 0 {
			t.Fatalf("status %+v %v", status, err)
		}
		if i < 3 && status.State != "queued" {
			t.Fatal(status)
		}
		if i == 3 && (status.State != "failed" || status.ErrorCode != "recovery_exhausted") {
			t.Fatal(status)
		}
		history, err := db.RecoveryHistory(ctx, job.ID)
		if err != nil || len(history) != i+1 {
			t.Fatalf("history %v %v", history, err)
		}
		body, _ := json.Marshal(history)
		if strings.Contains(string(body), claim.Token) || strings.Contains(string(body), "private-request") {
			t.Fatal("private content in history")
		}
	}
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	history, err := ro.RecoveryHistory(ctx, job.ID)
	if err != nil || len(history) != 4 || history[3].Reason != "recovery_limit" {
		t.Fatalf("restart %v %v", history, err)
	}
	if _, err = ro.RecoveryHistory(ctx, "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
}

func TestSubmissionRecoveryGuardsCancellationAndCapacity(t *testing.T) {
	for _, mode := range []string{"fresh", "config", "started", "cancel", "capacity"} {
		t.Run(mode, func(t *testing.T) {
			db, _ := submissionStore(t)
			ctx := context.Background()
			job := queuedSubmission(t, db, "job")
			claim := claimSubmission(t, db)
			now := time.Now().Add(2 * time.Minute)
			config := submitDigest("config")
			switch mode {
			case "fresh":
				now = time.Now()
			case "config":
				config = submitDigest("different")
			case "started":
				start := event("start", 1, runtime.TaskStarted)
				start.Data.SubmissionID = job.ID
				if err := db.AppendSubmission(ctx, 0, start, job.ID, claim.Token); err != nil {
					t.Fatal(err)
				}
			case "cancel":
				if _, err := db.CancelSubmission(ctx, job.ID); err != nil {
					t.Fatal(err)
				}
			case "capacity":
				for i := 0; i < 128; i++ {
					queuedSubmission(t, db, fmt.Sprint(i))
				}
			}
			ok, err := db.RecoverUndispatched(ctx, job.ID, config, now)
			if err != nil || ok != (mode == "cancel") {
				t.Fatalf("%v %v", ok, err)
			}
			status, err := db.Submission(ctx, job.ID)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "cancel" && (status.State != "canceled" || !status.CancelRequested || status.ErrorCode != "canceled") {
				t.Fatal(status)
			}
			history, err := db.RecoveryHistory(ctx, job.ID)
			if err != nil {
				t.Fatal(err)
			}
			if mode != "cancel" && len(history) != 0 {
				t.Fatal(history)
			}
		})
	}
}

func TestSubmissionRecoverySerializesWithTaskStart(t *testing.T) {
	for i := 0; i < 20; i++ {
		db, _ := submissionStore(t)
		ctx := context.Background()
		job := queuedSubmission(t, db, "job")
		claim := claimSubmission(t, db)
		start := event("start", 1, runtime.TaskStarted)
		start.Data.SubmissionID = job.ID
		var recovered bool
		var recoverErr, appendErr error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			recovered, recoverErr = db.RecoverUndispatched(ctx, job.ID, submitDigest("config"), time.Now().Add(2*time.Minute))
		}()
		go func() { defer wg.Done(); appendErr = db.AppendSubmission(ctx, 0, start, job.ID, claim.Token) }()
		wg.Wait()
		if recoverErr != nil || recovered == (appendErr == nil) {
			t.Fatalf("two winners or neither: recovery %v %v append %v", recovered, recoverErr, appendErr)
		}
		if recovered && !errors.Is(appendErr, runtime.ErrExecutionLeaseLost) {
			t.Fatal(appendErr)
		}
	}
}

func TestSubmissionRecoveryRollbackAndLegacy(t *testing.T) {
	db, path := submissionStore(t)
	ctx := context.Background()
	job := queuedSubmission(t, db, "job")
	claimSubmission(t, db)
	if _, err := db.db.Exec(`CREATE TRIGGER reject_recovery BEFORE UPDATE ON submissions WHEN NEW.state='queued' BEGIN SELECT RAISE(ABORT,'failure'); END`); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.RecoverUndispatched(ctx, job.ID, submitDigest("config"), time.Now().Add(2*time.Minute)); err == nil || ok {
		t.Fatal("expected rollback")
	}
	history, err := db.RecoveryHistory(ctx, job.ID)
	if err != nil || len(history) != 0 {
		t.Fatalf("partial audit %v %v", history, err)
	}
	if _, err = db.db.Exec(`DROP TRIGGER reject_recovery; DROP TABLE memory_retired_ids; DROP TABLE workflow_scan_buckets; DROP TABLE workflow_scan_consumptions; DROP TABLE workflow_scan_consumers; DROP TRIGGER workflow_scan_task_insert; DROP TABLE workflow_scan_tasks; DROP TABLE workflow_scan_pages; DROP TABLE workflow_scans; DROP TABLE workflow_selections; DROP TABLE skill_generation_attempts; DROP TABLE tool_approvals; DROP TABLE task_steering; DROP TABLE submission_recoveries; PRAGMA user_version=12`); err != nil {
		t.Fatal(err)
	}
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	history, err = ro.RecoveryHistory(ctx, job.ID)
	if err != nil || len(history) != 0 {
		t.Fatalf("legacy %v %v", history, err)
	}
	upgraded, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	if ok, err := upgraded.RecoverUndispatched(ctx, job.ID, submitDigest("config"), time.Now().Add(2*time.Minute)); err != nil || !ok {
		t.Fatalf("upgrade %v %v", ok, err)
	}
}

func TestSubmissionRecoveryRejectsCorruptHistory(t *testing.T) {
	db, _ := submissionStore(t)
	ctx := context.Background()
	job := queuedSubmission(t, db, "job")
	claimSubmission(t, db)
	if ok, err := db.RecoverUndispatched(ctx, job.ID, submitDigest("config"), time.Now().Add(2*time.Minute)); err != nil || !ok {
		t.Fatal(ok, err)
	}
	var original []byte
	if err := db.db.QueryRow(`SELECT body FROM submission_recoveries`).Scan(&original); err != nil {
		t.Fatal(err)
	}
	for _, body := range [][]byte{
		[]byte(strings.Replace(string(original), `"version":1`, `"version":1,"version":1`, 1)),
		[]byte(strings.Replace(string(original), `"version":1`, `"version":1,"unknown":true`, 1)),
		[]byte(strings.Replace(string(original), `"version":1`, `"Version":1`, 1)),
		[]byte(strings.Repeat("x", 4097)),
	} {
		if _, err := db.db.Exec(`UPDATE submission_recoveries SET body=?`, body); err != nil {
			t.Fatal(err)
		}
		if _, err := db.RecoveryHistory(ctx, job.ID); !errors.Is(err, submissions.ErrInvalid) {
			t.Fatalf("corrupt history accepted: %v", err)
		}
	}
}
