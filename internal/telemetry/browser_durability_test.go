package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/routing"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

const browserTestSubject = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestBrowserMigrationOperationCapacityPreservesPending(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	old := time.Now().UTC().Add(-BrowserOperationRetention - time.Hour).UnixNano()
	for i := 0; i < MaxBrowserOperations; i++ {
		id := fmt.Sprintf("op_%064x", i+1)
		_, err = store.db.Exec(`INSERT INTO browser_operations(operation_id,session_subject,key_digest,kind,request_digest,state,created_at,updated_at) VALUES(?,?,?,?,?,'pending',?,?)`, id, browserTestSubject, fmt.Sprintf("%064x", i+1), "submit", fmt.Sprintf("%064x", i+2), old, old)
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = store.BeginBrowserOperation(ctx, browserTestSubject, "browser-operation-capacity", "submit", []byte(`{"text":"new"}`))
	if !errors.Is(err, ErrBrowserOperationCapacity) {
		t.Fatal("hard cap not enforced", err)
	}
	var pending int
	if err = store.db.QueryRow(`SELECT count(*) FROM browser_operations WHERE state='pending'`).Scan(&pending); err != nil || pending != MaxBrowserOperations {
		t.Fatal("pending rows pruned", pending, err)
	}
	if _, err = store.db.Exec(`DELETE FROM browser_operations`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < MaxBrowserOperations; i++ {
		id := fmt.Sprintf("op_%064x", i+1)
		_, err = store.db.Exec(`INSERT INTO browser_operations(operation_id,session_subject,key_digest,kind,request_digest,state,response,created_at,updated_at) VALUES(?,?,?,?,?,'rejected','{}',?,?)`, id, browserTestSubject, fmt.Sprintf("%064x", i+1), "submit", fmt.Sprintf("%064x", i+2), old, old)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err = store.BeginBrowserOperation(ctx, browserTestSubject, "browser-operation-after-prune", "submit", []byte(`{"text":"new"}`)); err != nil {
		t.Fatal("rejected retention did not free capacity", err)
	}
	var rejected int
	if err = store.db.QueryRow(`SELECT count(*) FROM browser_operations WHERE state='rejected'`).Scan(&rejected); err != nil || rejected != 0 {
		t.Fatal("expired rejected rows retained", rejected, err)
	}
}

func TestBrowserOperationAdoptionTerminalizesAtCapacityAndAttributesRecovery(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	request := []byte(`{"version":1,"idempotency_key":"capacity-retry-key","action":"board.create"}`)
	target, err := store.BeginBrowserOperation(ctx, browserTestSubject, "capacity-retry-key", "board.create", request)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().UnixNano()
	for i := 1; i < MaxBrowserOperations; i++ {
		id := fmt.Sprintf("op_%064x", i)
		_, err = store.db.Exec(`INSERT INTO browser_operations(operation_id,session_subject,key_digest,kind,request_digest,state,created_at,updated_at) VALUES(?,?,?,?,?,'pending',?,?)`, id, browserTestSubject, fmt.Sprintf("%064x", i), "submit", fmt.Sprintf("%064x", i+1), now, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	recoverySubject := "1123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	adopted, found, err := store.AdoptableBrowserOperation(ctx, recoverySubject, "capacity-retry-key", "board.create", request)
	if err != nil || !found || adopted.OperationID != target.OperationID {
		t.Fatalf("adopted=%+v found=%t err=%v", adopted, found, err)
	}
	response := []byte(`{"version":1,"outcome":"committed"}`)
	committed, err := store.RecoverBrowserOperation(ctx, recoverySubject, adopted, "committed", response)
	if err != nil || committed.State != "committed" || committed.Subject != browserTestSubject {
		t.Fatalf("committed=%+v err=%v", committed, err)
	}
	recovery, err := store.BrowserOperationRecovery(ctx, target.OperationID)
	if err != nil || recovery.RecoverySubject != recoverySubject {
		t.Fatalf("recovery=%+v err=%v", recovery, err)
	}
	var pending, total int
	if err = store.db.QueryRow(`SELECT count(*) FROM browser_operations WHERE state='pending'`).Scan(&pending); err != nil || pending != MaxBrowserOperations-1 {
		t.Fatal("original row leaked pending capacity", pending, err)
	}
	if err = store.db.QueryRow(`SELECT count(*) FROM browser_operations`).Scan(&total); err != nil || total != MaxBrowserOperations {
		t.Fatal("adoption inserted or deleted operation row", total, err)
	}
	if _, err = store.BeginBrowserOperation(ctx, recoverySubject, "capacity-new-operation", "board.create", []byte(`{"version":1,"idempotency_key":"capacity-new-operation","action":"board.create"}`)); err != nil {
		t.Fatal("recovered terminal row did not free capacity under pressure", err)
	}
}

func TestBrowserOperationRecoveryMigratesFromSchema37(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`DROP TABLE legacy_browser_workboard_operations; DROP TABLE browser_operation_recoveries; DROP TRIGGER IF EXISTS workboard_auxiliary_review_settlement_immutable_delete; DROP TRIGGER IF EXISTS workboard_auxiliary_review_settlement_immutable_update; DROP TRIGGER IF EXISTS workboard_auxiliary_review_settlement_binding; DROP INDEX IF EXISTS workboard_auxiliary_review_settlements_board; DROP TABLE IF EXISTS workboard_auxiliary_review_settlements; DROP TRIGGER IF EXISTS workboard_auxiliary_review_admission_immutable_delete; DROP TRIGGER IF EXISTS workboard_auxiliary_review_admission_immutable_update; DROP TRIGGER IF EXISTS workboard_auxiliary_review_admission_binding; DROP INDEX IF EXISTS workboard_auxiliary_review_admissions_board; DROP TABLE IF EXISTS workboard_auxiliary_review_admissions; DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_settlement_binding; DROP INDEX IF EXISTS workboard_execution_settlements_board; DROP TABLE IF EXISTS workboard_execution_settlements; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_admission_binding; DROP TRIGGER IF EXISTS workboard_execution_admission_no_active; DROP INDEX IF EXISTS workboard_execution_admissions_card; DROP INDEX IF EXISTS workboard_execution_admissions_board; DROP INDEX IF EXISTS workboard_execution_admissions_global; DROP TABLE IF EXISTS workboard_execution_admissions; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_delete; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_update; DROP INDEX IF EXISTS workboard_task_start_claims_board; DROP TABLE IF EXISTS workboard_task_start_claims; PRAGMA user_version=37`); err != nil {
		t.Fatal(err)
	}
	legacy, err := store.BeginBrowserOperation(ctx, browserTestSubject, "legacy-schema37-pending", "board.create", []byte(`{"action":"board.create","idempotency_key":"legacy-schema37-pending"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var version int
	if err = reopened.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != currentStorageSchema {
		t.Fatal("schema 37 did not migrate", version, err)
	}
	var shape string
	if err = reopened.db.QueryRow(`SELECT group_concat(name||':'||type||':'||"notnull"||':'||pk,',') FROM pragma_table_info('browser_operation_recoveries')`).Scan(&shape); err != nil || shape != "operation_id:TEXT:0:1,recovery_subject:TEXT:1:0,recovered_at:INTEGER:1:0" {
		t.Fatal("recovery attribution table missing", shape, err)
	}
	if err = reopened.db.QueryRow(`SELECT group_concat(name||':'||type||':'||"notnull"||':'||pk,',') FROM pragma_table_info('legacy_browser_workboard_operations')`).Scan(&shape); err != nil || shape != "operation_id:TEXT:0:1" {
		t.Fatal("legacy provenance table missing", shape, err)
	}
	var legacyCount int
	if err = reopened.db.QueryRow(`SELECT count(*) FROM legacy_browser_workboard_operations WHERE operation_id=?`, legacy.OperationID).Scan(&legacyCount); err != nil || legacyCount != 1 {
		t.Fatal("schema-37 pending workboard operation not marked", legacyCount, err)
	}
	fresh, err := reopened.BeginBrowserOperation(ctx, browserTestSubject, "fresh-schema38-pending", "board.create", []byte(`{"action":"board.create","idempotency_key":"fresh-schema38-pending"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = reopened.db.QueryRow(`SELECT count(*) FROM legacy_browser_workboard_operations WHERE operation_id=?`, fresh.OperationID).Scan(&legacyCount); err != nil || legacyCount != 0 {
		t.Fatal("post-migration pending operation marked legacy", legacyCount, err)
	}
}

func TestBrowserMigrationRejectsPartialShapeAndRollsBack(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`DROP TABLE browser_feedback; DROP TABLE browser_operations; CREATE TABLE browser_operations(sentinel TEXT); DROP TRIGGER IF EXISTS workboard_auxiliary_review_settlement_immutable_delete; DROP TRIGGER IF EXISTS workboard_auxiliary_review_settlement_immutable_update; DROP TRIGGER IF EXISTS workboard_auxiliary_review_settlement_binding; DROP INDEX IF EXISTS workboard_auxiliary_review_settlements_board; DROP TABLE IF EXISTS workboard_auxiliary_review_settlements; DROP TRIGGER IF EXISTS workboard_auxiliary_review_admission_immutable_delete; DROP TRIGGER IF EXISTS workboard_auxiliary_review_admission_immutable_update; DROP TRIGGER IF EXISTS workboard_auxiliary_review_admission_binding; DROP INDEX IF EXISTS workboard_auxiliary_review_admissions_board; DROP TABLE IF EXISTS workboard_auxiliary_review_admissions; DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_settlement_binding; DROP INDEX IF EXISTS workboard_execution_settlements_board; DROP TABLE IF EXISTS workboard_execution_settlements; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_admission_binding; DROP TRIGGER IF EXISTS workboard_execution_admission_no_active; DROP INDEX IF EXISTS workboard_execution_admissions_card; DROP INDEX IF EXISTS workboard_execution_admissions_board; DROP INDEX IF EXISTS workboard_execution_admissions_global; DROP TABLE IF EXISTS workboard_execution_admissions; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_delete; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_update; DROP INDEX IF EXISTS workboard_task_start_claims_board; DROP TABLE IF EXISTS workboard_task_start_claims; PRAGMA user_version=33`); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, openErr := Open(ctx, path); openErr == nil {
		reopened.Close()
		t.Fatal("partial v34 schema accepted")
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var version int
	if err = raw.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 33 {
		t.Fatal("failed migration advanced", version, err)
	}
	var sentinel int
	if err = raw.QueryRow(`SELECT count(*) FROM pragma_table_info('browser_operations') WHERE name='sentinel'`).Scan(&sentinel); err != nil || sentinel != 1 {
		t.Fatal("partial table was replaced", sentinel, err)
	}
}

func TestBrowserFeedbackCoexistsAndCorruptionFailsClosed(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.Append(ctx, 0, event("start", 1, runtime.TaskStarted)); err != nil {
		t.Fatal(err)
	}
	turn := event("turn", 2, runtime.TurnStarted)
	turn.TurnID, turn.AttemptID, turn.Data.ModelID, turn.Data.ProviderID = "turn", "attempt", "model", "provider"
	if err = store.Append(ctx, 1, turn); err != nil {
		t.Fatal(err)
	}
	turnDone := event("turn-done", 3, runtime.TurnCompleted)
	turnDone.TurnID, turnDone.AttemptID = "turn", "attempt"
	if err = store.Append(ctx, 2, turnDone); err != nil {
		t.Fatal(err)
	}
	if err = store.Append(ctx, 3, event("done", 4, runtime.TaskCompleted)); err != nil {
		t.Fatal(err)
	}
	evidence := evaluation.Record{Version: 1, ID: "objective", TaskID: "task", AttemptID: "attempt", Key: routing.Key{Model: "model", Provider: "provider", Domain: "code", Profile: "default"}, Checks: []evaluation.Check{{Source: evaluation.Deterministic, Reference: "tests", Passed: true}, {Source: evaluation.LLMJudge, Reference: "audit", Passed: true}}, ExecutionSucceeded: true, Time: time.Now().UTC()}
	if err = store.RecordEvaluation(ctx, evidence); err != nil {
		t.Fatal(err)
	}
	first := BrowserFeedback{Version: 1, ID: "feedback_one", TaskID: "task", Accepted: true, AttemptCost: .2, CreatedAt: time.Now().UTC()}
	if err = store.AppendBrowserFeedback(ctx, first, 0); err != nil {
		t.Fatal(err)
	}
	second := BrowserFeedback{Version: 1, ID: "feedback_two", TaskID: "task", Supersedes: first.ID, Accepted: false, AttemptCost: .2, CreatedAt: first.CreatedAt.Add(time.Nanosecond)}
	if err = store.AppendBrowserFeedback(ctx, second, 1); err != nil {
		t.Fatal(err)
	}
	history, err := store.BrowserFeedbackHistory(ctx, "task")
	if err != nil || len(history) != 2 || history[1].Accepted {
		t.Fatal(history, err)
	}
	evaluationHistory, err := store.EvaluationHistory(ctx, "task", "attempt")
	if err != nil || len(evaluationHistory) != 1 || len(evaluationHistory[0].Checks) != 2 || evaluationHistory[0].Checks[0].Source != evaluation.Deterministic || evaluationHistory[0].Checks[1].Source != evaluation.LLMJudge {
		t.Fatal("evaluation evidence changed", evaluationHistory, err)
	}
	if _, err = store.db.Exec(`UPDATE browser_feedback SET body='{}' WHERE id=?`, first.ID); err != nil {
		t.Fatal(err)
	}
	if history, err = store.BrowserFeedbackHistory(ctx, "task"); !errors.Is(err, ErrBrowserFeedback) || history != nil {
		t.Fatal("corruption accepted", history, err)
	}
}
