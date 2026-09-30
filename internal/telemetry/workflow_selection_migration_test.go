package telemetry

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func TestWorkflowSelectionMigrationFrom16PreservesExistingData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	start := event("start", 1, runtime.TaskStarted)
	start.Time = start.Time.UTC()
	if err := s.Append(ctx, 0, start); err != nil {
		t.Fatal(err)
	}
	a := generationAttemptFixture("generation")
	if err := s.BeginSkillGeneration(ctx, a); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DROP TABLE task_timings; DROP TABLE task_timing_metadata; DROP INDEX events_task_kind; DROP TABLE skill_exposures; DROP INDEX task_heads_session; DROP TABLE learning_activation_intents; DROP TABLE lease_attention_history; DROP TABLE lease_attention; DROP TABLE lease_recoveries; ALTER TABLE resource_leases DROP COLUMN process_id; DROP TABLE lease_processes; DROP TABLE learning_states; DROP TABLE memory_retired_ids; DROP TABLE workflow_scan_buckets; DROP TABLE workflow_scan_consumptions; DROP TABLE workflow_scan_consumers; DROP TRIGGER workflow_scan_task_insert; DROP TABLE workflow_scan_tasks; DROP TABLE workflow_scan_pages; DROP TABLE workflow_scans; DROP TABLE workflow_selections; DROP TABLE IF EXISTS usage_corrections; DROP TABLE IF EXISTS usage_heads; DROP TABLE IF EXISTS usage_records; DROP TABLE IF EXISTS usage_metadata; DROP INDEX IF EXISTS evaluations_routing_key; DROP TRIGGER IF EXISTS workboard_auxiliary_review_settlement_immutable_delete; DROP TRIGGER IF EXISTS workboard_auxiliary_review_settlement_immutable_update; DROP TRIGGER IF EXISTS workboard_auxiliary_review_settlement_binding; DROP INDEX IF EXISTS workboard_auxiliary_review_settlements_board; DROP TABLE IF EXISTS workboard_auxiliary_review_settlements; DROP TRIGGER IF EXISTS workboard_auxiliary_review_admission_immutable_delete; DROP TRIGGER IF EXISTS workboard_auxiliary_review_admission_immutable_update; DROP TRIGGER IF EXISTS workboard_auxiliary_review_admission_binding; DROP INDEX IF EXISTS workboard_auxiliary_review_admissions_board; DROP TABLE IF EXISTS workboard_auxiliary_review_admissions; DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_settlement_binding; DROP INDEX IF EXISTS workboard_execution_settlements_board; DROP TABLE IF EXISTS workboard_execution_settlements; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_admission_binding; DROP TRIGGER IF EXISTS workboard_execution_admission_no_active; DROP INDEX IF EXISTS workboard_execution_admissions_card; DROP INDEX IF EXISTS workboard_execution_admissions_board; DROP INDEX IF EXISTS workboard_execution_admissions_global; DROP TABLE IF EXISTS workboard_execution_admissions; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_delete; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_update; DROP INDEX IF EXISTS workboard_task_start_claims_board; DROP TABLE IF EXISTS workboard_task_start_claims; PRAGMA user_version=16`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	var schema int
	if err := ro.db.QueryRow("PRAGMA user_version").Scan(&schema); err != nil || schema != 16 {
		t.Fatal("inspection migrated schema", schema, err)
	}
	if _, err := ro.ListWorkflowSelections(ctx, "project", "", 10); err == nil {
		t.Fatal("legacy store has selection table")
	}
	ro.Close()
	for range 2 {
		s, err = Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.db.QueryRow("PRAGMA user_version").Scan(&schema); err != nil || schema != currentStorageSchema {
			t.Fatal(schema, err)
		}
		events, err := s.Read(ctx, "task", 0, 10)
		if err != nil || len(events) != 1 || !reflect.DeepEqual(events[0], start) {
			t.Fatal("migration changed task", events, err)
		}
		generation, err := s.SkillGenerationAttempt(ctx, a.ID)
		if err != nil || !reflect.DeepEqual(generation, a) {
			t.Fatal("migration changed generation", generation, err)
		}
		items, err := s.ListWorkflowSelections(ctx, "project", "", 10)
		if err != nil || items == nil || len(items) != 0 {
			t.Fatal(items, err)
		}
		var index int
		if err := s.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND name='workflow_selections_scope'`).Scan(&index); err != nil || index != 1 {
			t.Fatal("missing selection index", index, err)
		}
		s.Close()
	}
}
