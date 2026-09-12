package telemetry

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func TestSkillGenerationMigrationFrom15PreservesExistingData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	start := event("start", 1, runtime.TaskStarted)
	start.Time = start.Time.UTC()
	if err = store.Append(ctx, 0, start); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`INSERT INTO tool_approvals VALUES('approval','task','call','pending',X'010203'); DROP TABLE task_timings; DROP TABLE task_timing_metadata; DROP INDEX events_task_kind; DROP TABLE skill_exposures; DROP INDEX task_heads_session; DROP TABLE learning_activation_intents; DROP TABLE lease_attention_history; DROP TABLE lease_attention; DROP TABLE lease_recoveries; ALTER TABLE resource_leases DROP COLUMN process_id; DROP TABLE lease_processes; DROP TABLE learning_states; DROP TABLE memory_retired_ids; DROP TABLE workflow_scan_buckets; DROP TABLE workflow_scan_consumptions; DROP TABLE workflow_scan_consumers; DROP TRIGGER workflow_scan_task_insert; DROP TABLE workflow_scan_tasks; DROP TABLE workflow_scan_pages; DROP TABLE workflow_scans; DROP TABLE workflow_selections; DROP TABLE skill_generation_attempts; DROP TABLE IF EXISTS usage_corrections; DROP TABLE IF EXISTS usage_heads; DROP TABLE IF EXISTS usage_records; DROP TABLE IF EXISTS usage_metadata; DROP INDEX IF EXISTS evaluations_routing_key; DROP TRIGGER IF EXISTS workboard_auxiliary_review_settlement_immutable_delete; DROP TRIGGER IF EXISTS workboard_auxiliary_review_settlement_immutable_update; DROP TRIGGER IF EXISTS workboard_auxiliary_review_settlement_binding; DROP INDEX IF EXISTS workboard_auxiliary_review_settlements_board; DROP TABLE IF EXISTS workboard_auxiliary_review_settlements; DROP TRIGGER IF EXISTS workboard_auxiliary_review_admission_immutable_delete; DROP TRIGGER IF EXISTS workboard_auxiliary_review_admission_immutable_update; DROP TRIGGER IF EXISTS workboard_auxiliary_review_admission_binding; DROP INDEX IF EXISTS workboard_auxiliary_review_admissions_board; DROP TABLE IF EXISTS workboard_auxiliary_review_admissions; DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_settlement_binding; DROP INDEX IF EXISTS workboard_execution_settlements_board; DROP TABLE IF EXISTS workboard_execution_settlements; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_admission_binding; DROP TRIGGER IF EXISTS workboard_execution_admission_no_active; DROP INDEX IF EXISTS workboard_execution_admissions_card; DROP INDEX IF EXISTS workboard_execution_admissions_board; DROP INDEX IF EXISTS workboard_execution_admissions_global; DROP TABLE IF EXISTS workboard_execution_admissions; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_delete; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_update; DROP INDEX IF EXISTS workboard_task_start_claims_board; DROP TABLE IF EXISTS workboard_task_start_claims; PRAGMA user_version=15`); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	// Inspection of the actual schema-15 fixture must not migrate it.
	readonly, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	var schema int
	if err = readonly.db.QueryRow("PRAGMA user_version").Scan(&schema); err != nil || schema != 15 {
		t.Fatalf("readonly migrated legacy database: %d %v", schema, err)
	}
	if err = readonly.Close(); err != nil {
		t.Fatal(err)
	}
	for reopen := range 2 {
		store, err = Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		if err = store.db.QueryRow("PRAGMA user_version").Scan(&schema); err != nil || schema != currentStorageSchema {
			t.Fatalf("migration schema=%d %v", schema, err)
		}
		items, err := store.Read(ctx, "task", 0, 10)
		if err != nil || len(items) != 1 || !reflect.DeepEqual(items[0], start) {
			t.Fatalf("event altered during migration: %+v %v", items, err)
		}
		var approval []byte
		if err = store.db.QueryRow(`SELECT body FROM tool_approvals WHERE id='approval'`).Scan(&approval); err != nil || !reflect.DeepEqual(approval, []byte{1, 2, 3}) {
			t.Fatalf("approval data altered: %v %v", approval, err)
		}
		var index int
		if err = store.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND name='skill_generation_attempts_scope'`).Scan(&index); err != nil || index != 1 {
			t.Fatalf("missing scope index: %d %v", index, err)
		}
		if reopen == 0 {
			if _, err = store.db.Exec(`INSERT INTO skill_generation_attempts(id,scope,name,status,body) VALUES('generation','project','checks','started',X'040506')`); err != nil {
				t.Fatal(err)
			}
		} else {
			var body []byte
			if err = store.db.QueryRow(`SELECT body FROM skill_generation_attempts WHERE id='generation' AND scope='project' AND name='checks' AND status='started'`).Scan(&body); err != nil || !reflect.DeepEqual(body, []byte{4, 5, 6}) {
				t.Fatalf("reopen changed generation data: %v %v", body, err)
			}
		}
		if err = store.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
