//go:build darwin || linux

package telemetry

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestTerminalReaderPageAdvancesMalformedAndHeld(t *testing.T) {
	s, token, kill := startTerminalReaderOwner(t, "completed")
	ctx := context.Background()
	now := time.Now().UTC()
	var original int64
	if err := s.db.QueryRow(`SELECT rowid FROM resource_leases WHERE token=?`, token).Scan(&original); err != nil {
		t.Fatal(err)
	}
	next, n, err := s.RecoverTerminalReadersPage(ctx, "", 1, now)
	if err != nil || n != 0 || next != strconv.FormatInt(original, 10) {
		t.Fatal(next, n, err)
	}
	if _, err := s.db.Exec(`INSERT INTO resource_leases(token,task_id,owner,scope,writer,expires,process_id) SELECT ? ,task_id,'other',scope,writer,expires,process_id FROM resource_leases WHERE token=?`, strings.Repeat("x", 513), token); err != nil {
		t.Fatal(err)
	}
	next, n, err = s.RecoverTerminalReadersPage(ctx, next, 1, now)
	if err != ErrLeaseRecovery || n != 0 || next != strconv.FormatInt(original+1, 10) {
		t.Fatal("malformed cursor stuck", next, n, err)
	}
	if _, err := s.db.Exec(`INSERT INTO resource_leases(token,task_id,owner,scope,writer,expires,process_id) SELECT 'valid-next',task_id,'third',scope,writer,expires,process_id FROM resource_leases WHERE token=?`, token); err != nil {
		t.Fatal(err)
	}
	kill()
	next, n, err = s.RecoverTerminalReadersPage(ctx, next, 1, now)
	if err != nil || n != 1 || next != strconv.FormatInt(original+2, 10) {
		t.Fatal("later row starved", next, n, err)
	}
	next, n, err = s.RecoverTerminalReadersPage(ctx, next, 1, now)
	if err != nil || n != 0 || next != "" {
		t.Fatal(next, n, err)
	}
	for _, cursor := range []string{"-1", "0", "01", "+1", "9223372036854775808", "token"} {
		if _, _, err = s.RecoverTerminalReadersPage(ctx, cursor, 1, now); err == nil {
			t.Fatal("invalid cursor", cursor)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	next, n, err = s.RecoverTerminalReadersPage(canceled, "7", 1, now)
	if err == nil || next != "7" || n != 0 {
		t.Fatal("lost error cursor", next, n, err)
	}
}

func TestTerminalReaderMigrationKeepsGuardBindings(t *testing.T) {
	s, token, kill := startTerminalReaderOwner(t, "completed")
	kill()
	ctx := context.Background()
	before, err := readRecoveryLease(ctx, s.db, token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`DROP TABLE task_timings; DROP TABLE task_timing_metadata; DROP INDEX events_task_kind; DROP TABLE skill_exposures; DROP INDEX task_heads_session; DROP TABLE learning_activation_intents; DROP TABLE lease_attention_history; DROP TABLE lease_attention; DROP TABLE lease_recoveries; DROP TABLE IF EXISTS usage_corrections; DROP TABLE IF EXISTS usage_heads; DROP TABLE IF EXISTS usage_records; DROP TABLE IF EXISTS usage_metadata; DROP INDEX IF EXISTS evaluations_routing_key; DROP TRIGGER IF EXISTS workboard_auxiliary_review_settlement_immutable_delete; DROP TRIGGER IF EXISTS workboard_auxiliary_review_settlement_immutable_update; DROP TRIGGER IF EXISTS workboard_auxiliary_review_settlement_binding; DROP INDEX IF EXISTS workboard_auxiliary_review_settlements_board; DROP TABLE IF EXISTS workboard_auxiliary_review_settlements; DROP TRIGGER IF EXISTS workboard_auxiliary_review_admission_immutable_delete; DROP TRIGGER IF EXISTS workboard_auxiliary_review_admission_immutable_update; DROP TRIGGER IF EXISTS workboard_auxiliary_review_admission_binding; DROP INDEX IF EXISTS workboard_auxiliary_review_admissions_board; DROP TABLE IF EXISTS workboard_auxiliary_review_admissions; DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_settlement_binding; DROP INDEX IF EXISTS workboard_execution_settlements_board; DROP TABLE IF EXISTS workboard_execution_settlements; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_admission_binding; DROP TRIGGER IF EXISTS workboard_execution_admission_no_active; DROP INDEX IF EXISTS workboard_execution_admissions_card; DROP INDEX IF EXISTS workboard_execution_admissions_board; DROP INDEX IF EXISTS workboard_execution_admissions_global; DROP TABLE IF EXISTS workboard_execution_admissions; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_delete; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_update; DROP INDEX IF EXISTS workboard_task_start_claims_board; DROP TABLE IF EXISTS workboard_task_start_claims; PRAGMA user_version=22`); err != nil {
		t.Fatal(err)
	}
	// initialize serializes the same schema22-to23 migration used by Open.
	if err = s.initialize(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := readRecoveryLease(ctx, s.db, token)
	if err != nil || after != before {
		t.Fatal("migration changed binding", err)
	}
	var version int
	if err = s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != currentStorageSchema {
		t.Fatal(version, err)
	}
	if recovered, err := s.RecoverTerminalReader(ctx, token, time.Now().UTC()); err != nil || !recovered {
		t.Fatal(recovered, err)
	}
}
