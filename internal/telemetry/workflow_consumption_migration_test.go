package telemetry

import (
	"context"
	"reflect"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func TestWorkflowConsumptionMigrationPreservesScanAndEvidence(t *testing.T) {
	ctx := context.Background()
	s, path := generationStore(t)
	workflowSourceFixture(t, s, "task-a", "session-a", "code", runtime.TaskCompleted, evaluation.UserFeedback, true, true)
	page, err := s.AdvanceWorkflowScan(ctx, "scope", "learning", "code", 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	selection := workflowSelectionFixture(t, "workflow")
	if _, err := s.SaveWorkflowSelection(ctx, selection); err != nil {
		t.Fatal(err)
	}
	before := workflowSourceRawBodies(t, s)
	var pageBody, headBody []byte
	if err := s.db.QueryRow(`SELECT body FROM workflow_scan_pages WHERE scope='scope' AND name='learning' AND revision=1`).Scan(&pageBody); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT body FROM workflow_scans WHERE scope='scope' AND name='learning'`).Scan(&headBody); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DROP TABLE task_timings; DROP TABLE task_timing_metadata; DROP INDEX events_task_kind; DROP TABLE skill_exposures; DROP INDEX task_heads_session; DROP TABLE learning_activation_intents; DROP TABLE lease_attention_history; DROP TABLE lease_attention; DROP TABLE lease_recoveries; ALTER TABLE resource_leases DROP COLUMN process_id; DROP TABLE lease_processes; DROP TABLE learning_states; DROP TABLE memory_retired_ids; DROP TABLE workflow_scan_buckets; DROP TABLE workflow_scan_consumptions; DROP TABLE workflow_scan_consumers; DROP TABLE IF EXISTS usage_corrections; DROP TABLE IF EXISTS usage_heads; DROP TABLE IF EXISTS usage_records; DROP TABLE IF EXISTS usage_metadata; DROP INDEX IF EXISTS evaluations_routing_key; DROP TRIGGER IF EXISTS workboard_auxiliary_review_settlement_immutable_delete; DROP TRIGGER IF EXISTS workboard_auxiliary_review_settlement_immutable_update; DROP TRIGGER IF EXISTS workboard_auxiliary_review_settlement_binding; DROP INDEX IF EXISTS workboard_auxiliary_review_settlements_board; DROP TABLE IF EXISTS workboard_auxiliary_review_settlements; DROP TRIGGER IF EXISTS workboard_auxiliary_review_admission_immutable_delete; DROP TRIGGER IF EXISTS workboard_auxiliary_review_admission_immutable_update; DROP TRIGGER IF EXISTS workboard_auxiliary_review_admission_binding; DROP INDEX IF EXISTS workboard_auxiliary_review_admissions_board; DROP TABLE IF EXISTS workboard_auxiliary_review_admissions; DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_settlement_binding; DROP INDEX IF EXISTS workboard_execution_settlements_board; DROP TABLE IF EXISTS workboard_execution_settlements; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_admission_binding; DROP TRIGGER IF EXISTS workboard_execution_admission_no_active; DROP INDEX IF EXISTS workboard_execution_admissions_card; DROP INDEX IF EXISTS workboard_execution_admissions_board; DROP INDEX IF EXISTS workboard_execution_admissions_global; DROP TABLE IF EXISTS workboard_execution_admissions; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_delete; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_update; DROP INDEX IF EXISTS workboard_task_start_claims_board; DROP TABLE IF EXISTS workboard_task_start_claims; PRAGMA user_version=18`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	var version, count int
	if err := ro.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 18 {
		t.Fatal(version, err)
	}
	if err := ro.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='workflow_scan_consumers'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("read-only open migrated", count, err)
	}
	ro.Close()
	if control, err := OpenWorkflowScanControl(ctx, path); err == nil || control != nil {
		if control != nil {
			control.Close()
		}
		t.Fatal("legacy write control admitted")
	}
	for range 2 {
		s, err = Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != currentStorageSchema {
			t.Fatal(version, err)
		}
		if !reflect.DeepEqual(before, workflowSourceRawBodies(t, s)) {
			t.Fatal("migration changed source evidence")
		}
		var savedPage, savedHead []byte
		if err := s.db.QueryRow(`SELECT body FROM workflow_scan_pages WHERE scope='scope' AND name='learning' AND revision=1`).Scan(&savedPage); err != nil {
			t.Fatal(err)
		}
		if err := s.db.QueryRow(`SELECT body FROM workflow_scans WHERE scope='scope' AND name='learning'`).Scan(&savedHead); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(pageBody, savedPage) || !reflect.DeepEqual(headBody, savedHead) {
			t.Fatal("migration changed scan receipts")
		}
		retry, err := s.AdvanceWorkflowScan(ctx, "scope", "learning", "code", 0, 1)
		if err != nil || !reflect.DeepEqual(retry, page) {
			t.Fatal("scan retry changed", retry, err)
		}
		savedSelection, err := s.WorkflowSelection(ctx, selection.Key.Scope, selection.ID)
		if err != nil || !reflect.DeepEqual(savedSelection, selection) {
			t.Fatal("selection changed", savedSelection, err)
		}
		if err := s.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('workflow_scan_consumers','workflow_scan_consumptions','workflow_scan_buckets')`).Scan(&count); err != nil || count != 3 {
			t.Fatal("consumer schema missing", count, err)
		}
		s.Close()
	}
}
