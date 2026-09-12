package telemetry

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/workboard"
	_ "modernc.org/sqlite"
)

func TestWorkboardReassignmentMigrationBackfillsExactLegacySuccessor(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 9, 9, 17, 0, 0, 123456789, time.UTC)
	fixture := recoverLifecycleClaim(t, ctx, store, &clock, 3)
	replacement := newTestLifecycleService(t, store, workboard.Actor{ID: "legacy-replacement", Type: "worker"},
		verifiedLifecycleRecovery("unused-migration-proof", workboard.EffectFree), &clock)
	if _, err = replacement.Claim(ctx, workboard.ClaimRequest{BoardID: fixture.boardID, CardID: fixture.cardID,
		IdempotencyKey: "legacy-replacement-key", ExpectedCardRevision: fixture.recoveredCardRevision}); err != nil {
		t.Fatal(err)
	}
	successorAttemptID, successorClaimID := currentLifecycleIDs(t, store, fixture.boardID, fixture.cardID)
	downgradeWorkboardReassignmentsTo39(t, store.db)
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	assertStoredReassignment(t, store, fixture, successorAttemptID, successorClaimID, clock)
	if _, err = store.db.Exec(`UPDATE workboard_reassignments SET created_at=created_at+1 WHERE recovery_id=?`, fixture.recoveryID); err == nil {
		t.Fatal("immutable reassignment was updated")
	}
	if _, err = store.db.Exec(`DELETE FROM workboard_reassignments WHERE recovery_id=?`, fixture.recoveryID); err == nil {
		t.Fatal("immutable reassignment was deleted")
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	assertStoredReassignment(t, restarted, fixture, successorAttemptID, successorClaimID, clock)
}

func TestWorkboardReassignmentMigrationLeavesUnassignedRecoveryUnlinked(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 9, 9, 18, 0, 0, 0, time.UTC)
	recoverLifecycleClaim(t, ctx, store, &clock, 3)
	downgradeWorkboardReassignmentsTo39(t, store.db)
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	assertReassignmentCount(t, store, 0)
}

func TestWorkboardReassignmentMigrationCorruptLegacySuccessorRollsBack(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 9, 9, 19, 0, 0, 0, time.UTC)
	fixture := recoverLifecycleClaim(t, ctx, store, &clock, 3)
	replacement := newTestLifecycleService(t, store, workboard.Actor{ID: "corrupt-legacy-replacement", Type: "worker"},
		verifiedLifecycleRecovery("unused-corrupt-proof", workboard.EffectFree), &clock)
	if _, err = replacement.Claim(ctx, workboard.ClaimRequest{BoardID: fixture.boardID, CardID: fixture.cardID,
		IdempotencyKey: "corrupt-legacy-key", ExpectedCardRevision: fixture.recoveredCardRevision}); err != nil {
		t.Fatal(err)
	}
	var recoveredAt int64
	if err = store.db.QueryRow(`SELECT recovered_at FROM workboard_recoveries WHERE id=?`, fixture.recoveryID).Scan(&recoveredAt); err != nil {
		t.Fatal(err)
	}
	successorAttemptID, _ := currentLifecycleIDs(t, store, fixture.boardID, fixture.cardID)
	downgradeWorkboardReassignmentsTo39(t, store.db)
	if _, err = store.db.Exec(`UPDATE workboard_attempts SET started_at=? WHERE id=?`, recoveredAt-1, successorAttemptID); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, openErr := Open(ctx, path); openErr == nil {
		reopened.Close()
		t.Fatal("corrupt legacy successor was backfilled")
	}
	assertRawReassignmentMigrationState(t, path, 39, false)
}

func TestWorkboardReassignmentMigrationRejectsForgedSchemaAtomically(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	downgradeWorkboardReassignmentsTo39(t, store.db)
	if _, err = store.db.Exec(`CREATE TABLE workboard_reassignments(sentinel TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, openErr := Open(ctx, path); openErr == nil {
		reopened.Close()
		t.Fatal("forged schema-40 table accepted")
	}
	assertRawReassignmentMigrationState(t, path, 39, true)
}

func downgradeWorkboardReassignmentsTo39(t *testing.T, db *sql.DB) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`DROP TRIGGER workboard_reassignment_immutable_delete;
		DROP TRIGGER workboard_reassignment_immutable_update;
		DROP INDEX workboard_reassignments_card;
		DROP TABLE workboard_reassignments;
		DROP INDEX workboard_recoveries_identity;
		DROP TRIGGER IF EXISTS workboard_auxiliary_review_settlement_immutable_delete; DROP TRIGGER IF EXISTS workboard_auxiliary_review_settlement_immutable_update; DROP TRIGGER IF EXISTS workboard_auxiliary_review_settlement_binding; DROP INDEX IF EXISTS workboard_auxiliary_review_settlements_board; DROP TABLE IF EXISTS workboard_auxiliary_review_settlements; DROP TRIGGER IF EXISTS workboard_auxiliary_review_admission_immutable_delete; DROP TRIGGER IF EXISTS workboard_auxiliary_review_admission_immutable_update; DROP TRIGGER IF EXISTS workboard_auxiliary_review_admission_binding; DROP INDEX IF EXISTS workboard_auxiliary_review_admissions_board; DROP TABLE IF EXISTS workboard_auxiliary_review_admissions; DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_settlement_binding; DROP INDEX IF EXISTS workboard_execution_settlements_board; DROP TABLE IF EXISTS workboard_execution_settlements; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_admission_binding; DROP TRIGGER IF EXISTS workboard_execution_admission_no_active; DROP INDEX IF EXISTS workboard_execution_admissions_card; DROP INDEX IF EXISTS workboard_execution_admissions_board; DROP INDEX IF EXISTS workboard_execution_admissions_global; DROP TABLE IF EXISTS workboard_execution_admissions; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_delete; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_update; DROP INDEX IF EXISTS workboard_task_start_claims_board; DROP TABLE IF EXISTS workboard_task_start_claims; PRAGMA user_version=39;`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func assertRawReassignmentMigrationState(t *testing.T, path string, version int, tablePresent bool) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var gotVersion, tables, indexes, triggers int
	if err = db.QueryRow(`PRAGMA user_version`).Scan(&gotVersion); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='workboard_reassignments'`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND name IN('workboard_reassignments_card','workboard_recoveries_identity')`).Scan(&indexes); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name LIKE 'workboard_reassignment_immutable_%'`).Scan(&triggers); err != nil {
		t.Fatal(err)
	}
	wantTables := 0
	if tablePresent {
		wantTables = 1
	}
	if gotVersion != version || tables != wantTables || indexes != 0 || triggers != 0 {
		t.Fatalf("raw migration state version=%d tables=%d indexes=%d triggers=%d", gotVersion, tables, indexes, triggers)
	}
}
