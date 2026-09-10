package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

func TestWorkboardExecutionBudgetSchema41MigrationIsEmptyAndRestartSafe(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`DROP TABLE workboard_execution_settlements;
		DROP TABLE workboard_execution_admissions; PRAGMA user_version=41`); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var version, admissions, settlements int
	if err = store.db.QueryRow(`SELECT
		(SELECT user_version FROM pragma_user_version),
		(SELECT count(*) FROM workboard_execution_admissions),
		(SELECT count(*) FROM workboard_execution_settlements)`).Scan(&version, &admissions, &settlements); err != nil || version != 42 || admissions != 0 || settlements != 0 {
		t.Fatalf("schema=%d admissions=%d settlements=%d err=%v", version, admissions, settlements, err)
	}
}

func TestWorkboardExecutionBudgetMigrationRejectsRetainedFutureTable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`PRAGMA user_version=41`); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, openErr := Open(ctx, path); openErr == nil {
		reopened.Close()
		t.Fatal("schema-41 database retained schema-42 accounting tables")
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var version int
	if err = raw.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 41 {
		t.Fatalf("failed migration changed schema=%d err=%v", version, err)
	}
}

func TestWorkboardExecutionBudgetBindingsActiveGateAndImmutability(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Unix(10, 0).UTC()
	digest := strings.Repeat("a", 64)
	start := runtime.Event{Version: 1, ID: "budget-start", TaskID: "task", SessionID: "session", CorrelationID: "task", WorkerID: "worker",
		Sequence: 1, Time: now, Kind: runtime.TaskStarted, Data: runtime.Data{ModelID: "model", ProviderID: "provider", ConfigID: digest}}
	startBody, _ := start.Encode()
	if _, err = store.db.Exec(`INSERT INTO task_heads(task_id,session_id,sequence,state) VALUES('task','session',1,'running');
		INSERT INTO events(id,task_id,sequence,body) VALUES('budget-start','task',1,?)`, startBody); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`INSERT INTO event_log(event_id,task_id,task_sequence,body_digest) VALUES('budget-start','task',1,?)`, streamBodyDigest(startBody)); err != nil {
		t.Fatal(err)
	}
	insertWorkboardFixture(t, store.db)
	if _, err = store.db.Exec(`INSERT INTO workboard_task_start_claims(task_id,session_id,event_id,event_digest,board_id,card_id,attempt_id,claim_id,worker_id,
		operation_id,request_digest,expected_card_revision,policy_digest,lease_ttl_ns,created_at,body)
		VALUES('task','session','budget-start',?,'board','card','attempt','claim','worker','operation',?,1,?,1000000,?, '{}')`,
		streamBodyDigest(startBody), digest, digest, now.UnixNano()); err != nil {
		t.Fatal(err)
	}
	admission := workboard.ExecutionAdmissionRecord{Version: 1, AdmissionID: "admission", TaskID: "task", SessionID: "session", EventID: start.ID,
		EventDigest: streamBodyDigest(startBody), BoardID: "board", CardID: "card", AttemptID: "attempt", ClaimID: "claim", WorkerID: "worker",
		OperationID: "operation", RequestDigest: digest, CardRevision: 1, ModelID: "model", ProviderID: "provider", ConfigID: digest,
		PolicyDigest: digest, TimeLimitMS: 1000, TokenLimit: 1000, CostMicros: 1000, GlobalWIPLimit: 2, BoardWIPLimit: 1, AdmittedAt: now}
	admission.AdmissionDigest, _ = admission.CanonicalDigest()
	admissionBody, _ := json.Marshal(admission)
	insertAdmission := `INSERT INTO workboard_execution_admissions(admission_id,task_id,session_id,event_id,event_digest,board_id,card_id,attempt_id,claim_id,
		worker_id,operation_id,request_digest,card_revision,model_id,provider_id,config_id,policy_digest,time_limit_ms,token_limit,cost_micros,
		global_wip_limit,board_wip_limit,admitted_at,admission_digest,body) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
	args := []any{admission.AdmissionID, admission.TaskID, admission.SessionID, admission.EventID, admission.EventDigest, admission.BoardID, admission.CardID,
		admission.AttemptID, admission.ClaimID, admission.WorkerID, admission.OperationID, admission.RequestDigest, admission.CardRevision, admission.ModelID,
		admission.ProviderID, admission.ConfigID, admission.PolicyDigest, admission.TimeLimitMS, admission.TokenLimit, admission.CostMicros,
		admission.GlobalWIPLimit, admission.BoardWIPLimit, admission.AdmittedAt.UnixNano(), admission.AdmissionDigest, admissionBody}
	var claimBound, eventBound bool
	if err = store.db.QueryRow(`SELECT
		EXISTS(SELECT 1 FROM workboard_task_start_claims c WHERE c.task_id=? AND c.session_id=? AND c.event_id=? AND c.event_digest=?
			AND c.board_id=? AND c.card_id=? AND c.attempt_id=? AND c.claim_id=? AND c.worker_id=? AND c.operation_id=? AND c.request_digest=?
			AND c.expected_card_revision=? AND c.policy_digest=?),
		EXISTS(SELECT 1 FROM events e JOIN event_log l ON l.event_id=e.id WHERE e.id=? AND e.task_id=? AND e.sequence=1
			AND l.task_id=? AND l.task_sequence=1 AND l.body_digest=? AND json_extract(e.body,'$.session_id')=?
			AND json_extract(e.body,'$.worker_id')=? AND json_extract(e.body,'$.kind')='task.started'
			AND json_extract(e.body,'$.data.model_id')=? AND json_extract(e.body,'$.data.provider_id')=?
			AND json_extract(e.body,'$.data.config_id')=?)`,
		admission.TaskID, admission.SessionID, admission.EventID, admission.EventDigest, admission.BoardID, admission.CardID, admission.AttemptID,
		admission.ClaimID, admission.WorkerID, admission.OperationID, admission.RequestDigest, admission.CardRevision, admission.PolicyDigest,
		admission.EventID, admission.TaskID, admission.TaskID, admission.EventDigest, admission.SessionID, admission.WorkerID, admission.ModelID, admission.ProviderID, admission.ConfigID).
		Scan(&claimBound, &eventBound); err != nil || !claimBound || !eventBound {
		t.Fatalf("fixture binding claim=%t event=%t err=%v", claimBound, eventBound, err)
	}
	badBinding := append([]any{}, args...)
	badBinding[14] = "other-provider"
	if _, err = store.db.Exec(insertAdmission, badBinding...); err == nil || !strings.Contains(err.Error(), "admission binding mismatch") {
		t.Fatalf("mismatched route binding err=%v", err)
	}
	badBinding = append([]any{}, args...)
	badBinding[15] = strings.Repeat("b", 64)
	if _, err = store.db.Exec(insertAdmission, badBinding...); err == nil || !strings.Contains(err.Error(), "admission binding mismatch") {
		t.Fatalf("mismatched configuration binding err=%v", err)
	}
	if _, err = store.db.Exec(insertAdmission, args...); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(insertAdmission, args...); err == nil || !strings.Contains(err.Error(), "active execution admission") {
		t.Fatalf("second active admission err=%v", err)
	}
	if _, err = store.db.Exec(`UPDATE workboard_execution_admissions SET model_id='forged' WHERE admission_id='admission'`); err == nil {
		t.Fatal("admission update accepted")
	}
	if _, err = store.db.Exec(`DELETE FROM workboard_execution_admissions WHERE admission_id='admission'`); err == nil {
		t.Fatal("admission delete accepted")
	}

	terminal := runtime.Event{Version: 1, ID: "budget-terminal", TaskID: "task", SessionID: "session", CorrelationID: "task", WorkerID: "worker",
		Sequence: 2, Time: now.Add(time.Second), Kind: runtime.TaskFailed}
	terminalBody, _ := terminal.Encode()
	if _, err = store.db.Exec(`INSERT INTO events(id,task_id,sequence,body) VALUES('budget-terminal','task',2,?)`, terminalBody); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`INSERT INTO event_log(event_id,task_id,task_sequence,body_digest) VALUES('budget-terminal','task',2,?)`, streamBodyDigest(terminalBody)); err != nil {
		t.Fatal(err)
	}
	settlement := workboard.ExecutionSettlementRecord{Version: 1, SettlementID: "settlement", AdmissionID: admission.AdmissionID,
		AdmissionDigest: admission.AdmissionDigest, TaskID: admission.TaskID, SessionID: admission.SessionID, BoardID: admission.BoardID, CardID: admission.CardID,
		AttemptID: admission.AttemptID, ClaimID: admission.ClaimID, WorkerID: admission.WorkerID, OperationID: admission.OperationID, ModelID: admission.ModelID,
		ProviderID: admission.ProviderID, ConfigID: admission.ConfigID, TerminalEventID: terminal.ID, TerminalEventDigest: streamBodyDigest(terminalBody),
		TerminalKind: terminal.Kind, TerminalSequence: terminal.Sequence, ChargedTimeMS: 1000, ChargedTokens: 1000, ChargedCostMicros: 1000,
		SettledAt: terminal.Time}
	settlement.SettlementDigest, _ = settlement.CanonicalDigest()
	settlementBody, _ := json.Marshal(settlement)
	insertSettlement := `INSERT INTO workboard_execution_settlements(settlement_id,admission_id,admission_digest,task_id,session_id,board_id,card_id,
		attempt_id,claim_id,worker_id,operation_id,model_id,provider_id,config_id,terminal_event_id,terminal_event_digest,terminal_kind,terminal_sequence,
		charged_time_ms,charged_tokens,charged_cost_micros,token_usage_known,settled_at,settlement_digest,body)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
	settlementArgs := []any{settlement.SettlementID, settlement.AdmissionID, settlement.AdmissionDigest, settlement.TaskID,
		settlement.SessionID, settlement.BoardID, settlement.CardID, settlement.AttemptID, settlement.ClaimID, settlement.WorkerID, settlement.OperationID,
		settlement.ModelID, settlement.ProviderID, settlement.ConfigID, settlement.TerminalEventID, settlement.TerminalEventDigest, settlement.TerminalKind,
		settlement.TerminalSequence, settlement.ChargedTimeMS, settlement.ChargedTokens, settlement.ChargedCostMicros, settlement.TokenUsageKnown,
		settlement.SettledAt.UnixNano(), settlement.SettlementDigest, settlementBody}
	if _, err = store.db.Exec(insertSettlement, settlementArgs...); err == nil || !strings.Contains(err.Error(), "settlement binding mismatch") {
		t.Fatalf("settlement before Workboard finalization err=%v", err)
	}
	if _, err = store.db.Exec(`UPDATE task_heads SET sequence=2,state='failed' WHERE task_id='task';
		UPDATE workboard_attempts SET state='failed',ended_at=10 WHERE id='attempt';
		UPDATE workboard_claims SET state='released',released_at=10 WHERE id='claim'`); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(insertSettlement, settlementArgs...); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`UPDATE workboard_execution_settlements SET charged_tokens=0 WHERE settlement_id='settlement'`); err == nil {
		t.Fatal("settlement update accepted")
	}
	if _, err = store.db.Exec(`DELETE FROM workboard_execution_settlements WHERE settlement_id='settlement'`); err == nil {
		t.Fatal("settlement delete accepted")
	}
}
