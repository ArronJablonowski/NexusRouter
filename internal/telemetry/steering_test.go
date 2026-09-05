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

	"github.com/ArronJablonowski/DarwinRouter/runtime"
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
	if _, err = db.db.Exec(`DROP TABLE task_steering; PRAGMA user_version=13`); err != nil {
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
