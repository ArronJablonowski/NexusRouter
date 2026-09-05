package telemetry

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func TestWorkflowScanMigrationFrom17BackfillsAndPreservesHistory(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	workflowSourceFixture(t, s, "task-z", "session-z", "code", runtime.TaskCompleted, evaluation.UserFeedback, true, true)
	workflowSourceFixture(t, s, "task-a", "session-a", "code", runtime.TaskCompleted, evaluation.UserFeedback, true, true)
	selection := workflowSelectionFixture(t, "workflow")
	if _, err := s.SaveWorkflowSelection(ctx, selection); err != nil {
		t.Fatal(err)
	}
	before := workflowSourceRawBodies(t, s)
	if _, err := s.db.Exec(`ALTER TABLE resource_leases DROP COLUMN process_id; DROP TABLE lease_processes; DROP TABLE learning_states; DROP TABLE memory_retired_ids; DROP TABLE workflow_scan_buckets; DROP TABLE workflow_scan_consumptions; DROP TABLE workflow_scan_consumers; DROP TRIGGER workflow_scan_task_insert; DROP TABLE workflow_scan_tasks; DROP TABLE workflow_scan_pages; DROP TABLE workflow_scans; PRAGMA user_version=17`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	var version, tables int
	if err := ro.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 17 {
		t.Fatal(version, err)
	}
	if err := ro.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='workflow_scan_tasks'`).Scan(&tables); err != nil || tables != 0 {
		t.Fatal("readonly migrated", tables, err)
	}
	ro.Close()
	for range 2 {
		s, err = Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 22 {
			t.Fatal(version, err)
		}
		if !reflect.DeepEqual(before, workflowSourceRawBodies(t, s)) {
			t.Fatal("migration rewrote historical evidence")
		}
		saved, err := s.WorkflowSelection(ctx, selection.Key.Scope, selection.ID)
		if err != nil || !reflect.DeepEqual(saved, selection) {
			t.Fatal("selection changed", saved, err)
		}
		var first, second string
		if err := s.db.QueryRow(`SELECT task_id FROM workflow_scan_tasks WHERE seq=1`).Scan(&first); err != nil {
			t.Fatal(err)
		}
		if err := s.db.QueryRow(`SELECT task_id FROM workflow_scan_tasks WHERE seq=2`).Scan(&second); err != nil {
			t.Fatal(err)
		}
		if first != "task-a" || second != "task-z" {
			t.Fatal("unstable backfill", first, second)
		}
		s.Close()
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	workflowSourceFixture(t, s, "task-0", "session-0", "code", runtime.TaskCompleted, evaluation.UserFeedback, true, true)
	var sequence int64
	if err := s.db.QueryRow(`SELECT seq FROM workflow_scan_tasks WHERE task_id='task-0'`).Scan(&sequence); err != nil || sequence != 3 {
		t.Fatal("new lower ID lacks insertion fence", sequence, err)
	}
}

func TestWorkflowScanInsertionFenceIsTransactional(t *testing.T) {
	s, _ := generationStore(t)
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO task_heads VALUES('rollback-task','session',1,'running')`); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := tx.QueryRow(`SELECT count(*) FROM workflow_scan_tasks WHERE task_id='rollback-task'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("trigger absent", count, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM workflow_scan_tasks WHERE task_id='rollback-task'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("rolled-back task retained scan membership", count, err)
	}
}
