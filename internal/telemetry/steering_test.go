package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func TestSteeringAtomicApplyAndIdempotence(t *testing.T) {
	db, path := submissionStore(t)
	ctx := context.Background()
	if err := db.Append(ctx, 0, event("start", 1, runtime.TaskStarted)); err != nil {
		t.Fatal(err)
	}
	message, err := db.QueueSteering(ctx, "task", submitDigest("key"), "new instruction")
	if err != nil {
		t.Fatal(err)
	}
	same, err := db.QueueSteering(ctx, "task", submitDigest("key"), "new instruction")
	if err != nil || !reflect.DeepEqual(same, message) {
		t.Fatal(same, err)
	}
	if _, err = db.QueueSteering(ctx, "task", submitDigest("key"), "different"); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	complete := event("complete", 2, runtime.TaskCompleted)
	if err = db.Append(ctx, 1, complete); !errors.Is(err, runtime.ErrSteeringPending) {
		t.Fatal(err)
	}
	failed := event("failed", 2, runtime.TaskFailed)
	failed.Data.Code = "provider_retryable_no_output"
	if err = db.Append(ctx, 1, failed); !errors.Is(err, runtime.ErrSteeringPending) {
		t.Fatal(err)
	}
	failed.Data.Code = "provider_failed_before_tools"
	if err = db.Append(ctx, 1, failed); !errors.Is(err, runtime.ErrSteeringPending) {
		t.Fatal(err)
	}
	applied := event("applied", 2, runtime.SteeringApplied)
	applied.Data.SteeringID = message.ID
	applied.Data.Text = "wrong"
	if err = db.Append(ctx, 1, applied); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	applied.Data.Text = message.Text
	if err = db.Append(ctx, 1, applied); err != nil {
		t.Fatal(err)
	}
	if err = db.Append(ctx, 1, applied); err != nil {
		t.Fatal("exact retry", err)
	}
	status, err := db.SteeringStatus(ctx, "task", message.ID)
	if err != nil || status.State != "applied" || status.AppliedSequence == nil || *status.AppliedSequence != 2 {
		t.Fatal(status, err)
	}
	reapplied := applied
	reapplied.ID = "again"
	reapplied.Sequence = 3
	if err = db.Append(ctx, 2, reapplied); !errors.Is(err, ErrConflict) {
		t.Fatal("double consume", err)
	}
	complete.Sequence = 3
	if err = db.Append(ctx, 2, complete); err != nil {
		t.Fatal(err)
	}
	same, err = db.QueueSteering(ctx, "task", submitDigest("key"), "new instruction")
	if err != nil || same.State != "applied" {
		t.Fatal("closed retry", same, err)
	}
	if _, err = db.QueueSteering(ctx, "task", submitDigest("next"), "new"); !errors.Is(err, runtime.ErrSteeringClosed) {
		t.Fatal(err)
	}
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if next, err := ro.NextSteering(ctx, "task"); err != nil || next != nil {
		t.Fatal(next, err)
	}
	if _, err = ro.SteeringStatus(ctx, "task", message.ID); err != nil {
		t.Fatal(err)
	}
}

func TestSteeringRevisionCASReplaysBeforeStaleCheck(t *testing.T) {
	db, _ := submissionStore(t)
	ctx := context.Background()
	if err := db.Append(ctx, 0, event("start", 1, runtime.TaskStarted)); err != nil {
		t.Fatal(err)
	}
	key := submitDigest("revision-key")
	if _, err := db.QueueSteeringAtRevision(ctx, "task", key, "guide", 2); !errors.Is(err, ErrConflict) {
		t.Fatal("stale revision accepted", err)
	}
	message, err := db.QueueSteeringAtRevision(ctx, "task", key, "guide", 1)
	if err != nil {
		t.Fatal(err)
	}
	applied := event("applied-revision", 2, runtime.SteeringApplied)
	applied.Data.SteeringID, applied.Data.Text = message.ID, message.Text
	if err = db.Append(ctx, 1, applied); err != nil {
		t.Fatal(err)
	}
	replayed, err := db.QueueSteeringAtRevision(ctx, "task", key, "guide", 1)
	if err != nil || replayed.State != "applied" {
		t.Fatal("acknowledgement retry did not converge", replayed, err)
	}
}

func TestSteeringLimitCancellationAndRollback(t *testing.T) {
	db, _ := submissionStore(t)
	ctx := context.Background()
	if err := db.Append(ctx, 0, event("start", 1, runtime.TaskStarted)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 32; i++ {
		if _, err := db.QueueSteering(ctx, "task", submitDigest(fmt.Sprint(i)), fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.QueueSteering(ctx, "task", submitDigest("extra"), "extra"); !errors.Is(err, runtime.ErrSteeringLimit) {
		t.Fatal(err)
	}
	next, err := db.NextSteering(ctx, "task")
	if err != nil || next.Text != "0" {
		t.Fatal(next, err)
	}
	if _, err = db.db.Exec(`CREATE TRIGGER reject_steer BEFORE INSERT ON events WHEN NEW.sequence=2 BEGIN SELECT RAISE(ABORT,'failure'); END`); err != nil {
		t.Fatal(err)
	}
	applied := event("applied", 2, runtime.SteeringApplied)
	applied.Data.SteeringID = next.ID
	applied.Data.Text = next.Text
	if err = db.Append(ctx, 1, applied); err == nil {
		t.Fatal("expected rollback")
	}
	status, err := db.SteeringStatus(ctx, "task", next.ID)
	if err != nil || status.State != "pending" || status.AppliedSequence != nil {
		t.Fatal(status, err)
	}
	if _, err = db.RequestCancellation(ctx, "task"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.QueueSteering(ctx, "task", submitDigest("canceled"), "new"); !errors.Is(err, runtime.ErrSteeringClosed) {
		t.Fatal(err)
	}
	if _, err = db.db.Exec(`DROP TRIGGER reject_steer`); err != nil {
		t.Fatal(err)
	}
	if err = db.Append(ctx, 1, event("cancel", 2, runtime.TaskCanceled)); err != nil {
		t.Fatal(err)
	}
	if next, err = db.NextSteering(ctx, "task"); err != nil || next == nil {
		t.Fatal("pending lost after cancel", next, err)
	}
}

func TestSteeringCompletionRace(t *testing.T) {
	for i := 0; i < 20; i++ {
		db, _ := submissionStore(t)
		ctx := context.Background()
		if err := db.Append(ctx, 0, event("start", 1, runtime.TaskStarted)); err != nil {
			t.Fatal(err)
		}
		var queueErr, completeErr error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, queueErr = db.QueueSteering(ctx, "task", submitDigest("key"), "instruction")
		}()
		go func() { defer wg.Done(); completeErr = db.Append(ctx, 1, event("complete", 2, runtime.TaskCompleted)) }()
		wg.Wait()
		if (queueErr == nil) == (completeErr == nil) {
			t.Fatal("both/neither won", queueErr, completeErr)
		}
		if queueErr != nil && !errors.Is(queueErr, runtime.ErrSteeringClosed) {
			t.Fatal(queueErr)
		}
		if completeErr != nil && !errors.Is(completeErr, runtime.ErrSteeringPending) {
			t.Fatal(completeErr)
		}
	}
}

func TestSteeringLegacyAndMalformed(t *testing.T) {
	db, path := submissionStore(t)
	ctx := context.Background()
	if err := db.Append(ctx, 0, event("start", 1, runtime.TaskStarted)); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"", " ", strings.Repeat("x", 65537), string([]byte{255})} {
		if _, err := db.QueueSteering(ctx, "task", submitDigest("key"), text); err == nil {
			t.Fatal("invalidtext accepted")
		}
	}
	if _, err := db.QueueSteering(ctx, "task", "bad-hash", "instruction"); err == nil {
		t.Fatal("invalidhash")
	}
	if _, err := db.QueueSteering(ctx, "missing", submitDigest("key"), "instruction"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	m, err := db.QueueSteering(ctx, "task", submitDigest("key"), "instruction")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.db.Exec(`UPDATE task_steering SET state='unknown'`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.NextSteering(ctx, "task"); err == nil {
		t.Fatal("corruptrecord hidden")
	}
	if err = db.Append(ctx, 1, event("complete", 2, runtime.TaskCompleted)); err == nil {
		t.Fatal("corruptsteering allowedcompletion")
	}
	if _, err = db.db.Exec(`DROP TABLE task_timings; DROP TABLE task_timing_metadata; DROP INDEX events_task_kind; DROP TABLE skill_exposures; DROP INDEX task_heads_session; DROP TABLE learning_activation_intents; DROP TABLE lease_attention_history; DROP TABLE lease_attention; DROP TABLE lease_recoveries; ALTER TABLE resource_leases DROP COLUMN process_id; DROP TABLE lease_processes; DROP TABLE learning_states; DROP TABLE memory_retired_ids; DROP TABLE workflow_scan_buckets; DROP TABLE workflow_scan_consumptions; DROP TABLE workflow_scan_consumers; DROP TRIGGER workflow_scan_task_insert; DROP TABLE workflow_scan_tasks; DROP TABLE workflow_scan_pages; DROP TABLE workflow_scans; DROP TABLE workflow_selections; DROP TABLE skill_generation_attempts; DROP TABLE tool_approvals; DROP TABLE task_steering; DROP TABLE IF EXISTS usage_corrections; DROP TABLE IF EXISTS usage_heads; DROP TABLE IF EXISTS usage_records; DROP TABLE IF EXISTS usage_metadata; DROP INDEX IF EXISTS evaluations_routing_key; DROP TRIGGER IF EXISTS workboard_auxiliary_review_settlement_immutable_delete; DROP TRIGGER IF EXISTS workboard_auxiliary_review_settlement_immutable_update; DROP TRIGGER IF EXISTS workboard_auxiliary_review_settlement_binding; DROP INDEX IF EXISTS workboard_auxiliary_review_settlements_board; DROP TABLE IF EXISTS workboard_auxiliary_review_settlements; DROP TRIGGER IF EXISTS workboard_auxiliary_review_admission_immutable_delete; DROP TRIGGER IF EXISTS workboard_auxiliary_review_admission_immutable_update; DROP TRIGGER IF EXISTS workboard_auxiliary_review_admission_binding; DROP INDEX IF EXISTS workboard_auxiliary_review_admissions_board; DROP TABLE IF EXISTS workboard_auxiliary_review_admissions; DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_settlement_binding; DROP INDEX IF EXISTS workboard_execution_settlements_board; DROP TABLE IF EXISTS workboard_execution_settlements; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_admission_binding; DROP TRIGGER IF EXISTS workboard_execution_admission_no_active; DROP INDEX IF EXISTS workboard_execution_admissions_card; DROP INDEX IF EXISTS workboard_execution_admissions_board; DROP INDEX IF EXISTS workboard_execution_admissions_global; DROP TABLE IF EXISTS workboard_execution_admissions; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_delete; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_update; DROP INDEX IF EXISTS workboard_task_start_claims_board; DROP TABLE IF EXISTS workboard_task_start_claims; PRAGMA user_version=13`); err != nil {
		t.Fatal(err)
	}
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if next, err := ro.NextSteering(ctx, "task"); err != nil || next != nil {
		t.Fatal(next, err)
	}
	if _, err = ro.SteeringStatus(ctx, "task", m.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	upgraded, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	if _, err = upgraded.QueueSteering(ctx, "task", submitDigest("new"), "instruction"); err != nil {
		t.Fatal(err)
	}
}
