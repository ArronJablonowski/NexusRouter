package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func TestListSteeringReadOnlyOrderedClosedTask(t *testing.T) {
	db, path := submissionStore(t)
	ctx := context.Background()
	if err := db.Append(ctx, 0, event("start", 1, runtime.TaskStarted)); err != nil {
		t.Fatal(err)
	}
	first, err := db.QueueSteering(ctx, "task", submitDigest("first"), "first guidance")
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.QueueSteering(ctx, "task", submitDigest("second"), "second guidance")
	if err != nil {
		t.Fatal(err)
	}
	e := event("apply", 2, runtime.SteeringApplied)
	e.Data.SteeringID, e.Data.Text = first.ID, first.Text
	if err := db.Append(ctx, 1, e); err != nil {
		t.Fatal(err)
	}
	if err := db.Append(ctx, 2, event("cancel", 3, runtime.TaskCanceled)); err != nil {
		t.Fatal(err)
	}
	first, err = db.SteeringStatus(ctx, "task", first.ID)
	if err != nil {
		t.Fatal(err)
	}
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	before, err := db.Read(ctx, "task", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	want := []runtime.SteeringMessage{first, second}
	for i := 0; i < 2; i++ {
		got, err := ro.ListSteering(ctx, "task")
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatal(got, err)
		}
	}
	after, err := db.Read(ctx, "task", 0, 100)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("listing mutated history", err)
	}
	if _, err := ro.ListSteering(ctx, "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
}

func TestListSteeringLegacyDoesNotMigrate(t *testing.T) {
	db, path := submissionStore(t)
	ctx := context.Background()
	if err := db.Append(ctx, 0, event("start", 1, runtime.TaskStarted)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`DROP TABLE task_timings; DROP TABLE task_timing_metadata; DROP INDEX events_task_kind; DROP TABLE skill_exposures; DROP INDEX task_heads_session; DROP TABLE learning_activation_intents; DROP TABLE lease_attention_history; DROP TABLE lease_attention; DROP TABLE lease_recoveries; ALTER TABLE resource_leases DROP COLUMN process_id; DROP TABLE lease_processes; DROP TABLE learning_states; DROP TABLE memory_retired_ids; DROP TABLE workflow_scan_buckets; DROP TABLE workflow_scan_consumptions; DROP TABLE workflow_scan_consumers; DROP TRIGGER workflow_scan_task_insert; DROP TABLE workflow_scan_tasks; DROP TABLE workflow_scan_pages; DROP TABLE workflow_scans; DROP TABLE workflow_selections; DROP TABLE skill_generation_attempts; DROP TABLE tool_approvals; DROP TABLE task_steering; DROP TABLE IF EXISTS usage_corrections; DROP TABLE IF EXISTS usage_heads; DROP TABLE IF EXISTS usage_records; DROP TABLE IF EXISTS usage_metadata; DROP INDEX IF EXISTS evaluations_routing_key; PRAGMA user_version=13`); err != nil {
		t.Fatal(err)
	}
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	got, err := ro.ListSteering(ctx, "task")
	if err != nil || got == nil || len(got) != 0 {
		t.Fatal(got, err)
	}
	if _, err := ro.ListSteering(ctx, "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	var version, count int
	if err := ro.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 13 {
		t.Fatal(version, err)
	}
	if err := ro.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='task_steering'").Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}

func TestListSteeringRejectsCorruptionWithoutPartialResults(t *testing.T) {
	for _, tc := range []struct {
		name, query string
		value       any
	}{
		{"state", `UPDATE task_steering SET state=?`, "unknown"},
		{"pending counter", `UPDATE task_steering SET applied_sequence=?`, 2},
		{"future applied counter", `UPDATE task_steering SET state='applied',applied_sequence=?`, 50},
		{"invalid applied counter", `UPDATE task_steering SET state='applied',applied_sequence=?`, 1},
		{"oversize id", `UPDATE task_steering SET id=?`, strings.Repeat("x", 129)},
		{"oversize text", `UPDATE task_steering SET text=?`, strings.Repeat("x", 65537)},
		{"empty text", `UPDATE task_steering SET text=?`, ""},
		{"time", `UPDATE task_steering SET created_at=?`, "bad"},
		{"head state", `UPDATE task_heads SET state=?`, "unknown"},
		{"head counter", `UPDATE task_heads SET sequence=?`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, _ := submissionStore(t)
			ctx := context.Background()
			if err := db.Append(ctx, 0, event("start", 1, runtime.TaskStarted)); err != nil {
				t.Fatal(err)
			}
			if _, err := db.QueueSteering(ctx, "task", submitDigest("key"), "guidance"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.db.Exec(tc.query, tc.value); err != nil {
				t.Fatal(err)
			}
			if got, err := db.ListSteering(ctx, "task"); err == nil || got != nil {
				t.Fatal(got, err)
			}
		})
	}
}

func TestListSteeringLifetimeLimit(t *testing.T) {
	db, _ := submissionStore(t)
	ctx := context.Background()
	if err := db.Append(ctx, 0, event("start", 1, runtime.TaskStarted)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 32; i++ {
		if _, err := db.QueueSteering(ctx, "task", submitDigest(fmt.Sprint(i)), "guidance"); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := db.ListSteering(ctx, "task"); err != nil || len(got) != 32 {
		t.Fatal(len(got), err)
	}
	if _, err := db.db.Exec(`INSERT INTO task_steering SELECT 'extra',task_id,'extra-key',text,state,created_at,applied_sequence FROM task_steering LIMIT 1`); err != nil {
		t.Fatal(err)
	}
	if got, err := db.ListSteering(ctx, "task"); err == nil || got != nil {
		t.Fatal(got, err)
	}
}
