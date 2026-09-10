package telemetry

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

var workboardTables = []string{
	"workboard_task_start_claims",
	"workboard_events",
	"workboard_operations",
	"workboard_reassignments",
	"workboard_recoveries",
	"workboard_recovery_proofs",
	"workboard_acceptances",
	"workboard_evidence",
	"workboard_candidate_artifacts",
	"workboard_candidates",
	"workboard_checkpoints",
	"workboard_claim_heartbeats",
	"workboard_claims",
	"workboard_criteria",
	"workboard_attempt_sessions",
	"workboard_attempt_tasks",
	"workboard_attempts",
	"workboard_dependencies",
	"workboard_card_labels",
	"workboard_cards",
	"workboard_columns",
	"workboard_boards",
}

func downgradeWorkboards(t *testing.T, db *sql.DB) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, table := range workboardTables {
		if _, err = tx.Exec("DROP TABLE " + table); err != nil {
			t.Fatal(table, err)
		}
	}
	if _, err = tx.Exec("DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_delete; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_update; DROP INDEX IF EXISTS workboard_task_start_claims_board; DROP TABLE IF EXISTS workboard_task_start_claims; PRAGMA user_version=34"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func insertWorkboardFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	digest := strings.Repeat("a", 64)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO workboard_boards
		(id,revision,layout_revision,event_sequence,graph_revision,graph_digest,state,title,description,card_count,active_claims,created_at,updated_at,body)
		VALUES('board',1,1,1,1,?,'active','Board','',2,1,1,1,'{}')`, digest); err != nil {
		t.Fatal(err)
	}
	columns := []struct {
		state, title string
	}{
		{"backlog", "Backlog"}, {"ready", "Ready"}, {"in_progress", "In Progress"},
		{"blocked", "Blocked"}, {"review", "Review"}, {"done", "Done"}, {"canceled", "Canceled"},
	}
	for ordinal, column := range columns {
		if _, err = tx.Exec(`INSERT INTO workboard_columns(board_id,version,id,state,ordinal,rank,title) VALUES('board',1,?,?,?,?,?)`, column.state, column.state, ordinal, fmt.Sprint(ordinal), column.title); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = tx.Exec(`INSERT INTO workboard_cards
		(id,board_id,revision,criteria_revision,state,rank,title,description,priority,assignee_id,remaining_dependencies,attempt_count,attempt_limit,time_limit_ms,token_limit,cost_micros,current_attempt_id,current_claim_id,cancel_requested,pause_requested,created_at,updated_at,body)
		VALUES('card','board',1,1,'in_progress','a','Running card','','high','worker',0,1,3,60000,1000,1000,'attempt','claim',0,0,1,1,'{"pause_requested":false}')`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`INSERT INTO workboard_cards
		(id,board_id,revision,criteria_revision,state,rank,title,description,priority,remaining_dependencies,attempt_count,attempt_limit,time_limit_ms,token_limit,cost_micros,cancel_requested,pause_requested,created_at,updated_at,body)
		VALUES('successor','board',1,1,'backlog','b','Successor','','normal',1,0,3,60000,1000,1000,0,0,1,1,'{"pause_requested":false}')`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`INSERT INTO workboard_dependencies(board_id,card_id,dependency_id,created_sequence) VALUES('board','successor','card',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`INSERT INTO workboard_criteria
		(board_id,card_id,criteria_revision,ordinal,id,kind,required_source,validator_id,description,required,body)
		VALUES('board','card',1,0,'criterion','objective','deterministic','validator','Pass tests',1,'{}')`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`INSERT INTO workboard_attempts
		(id,board_id,card_id,ordinal,revision,state,worker_id,criteria_revision,criteria_digest,policy_digest,attempt_limit,time_limit_ms,token_limit,cost_micros,started_at,body)
		VALUES('attempt','board','card',1,1,'running','worker',1,?,?,3,60000,1000,1000,1,'{}')`, digest, digest); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`INSERT INTO workboard_claims
		(id,board_id,card_id,attempt_id,revision,state,owner_id,owner_type,task_id,expires_at,last_heartbeat,body)
		VALUES('claim','board','card','attempt',1,'active','worker','worker','task',2,1,'{}')`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`INSERT INTO workboard_claim_heartbeats(claim_id,revision,observed_at,expires_at,actor_id,body)
		VALUES('claim',1,1,2,'worker','{}')`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`INSERT INTO workboard_checkpoints
		(id,board_id,card_id,attempt_id,claim_id,revision,claim_revision,criteria_revision,criteria_digest,policy_digest,evidence,evidence_digest,actor_id,actor_type,created_at,body)
		VALUES('checkpoint','board','card','attempt','claim',1,1,1,?,?,'progress',?,'worker','worker',2,'{}')`, digest, digest, digest); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`INSERT INTO workboard_operations
		(scope_kind,scope_id,key_digest,operation_id,board_id,request_digest,response_digest,first_sequence,last_sequence,event_count,transaction_bytes,outcome,response,created_at)
		VALUES('creation','session',?,'operation','board',?,?,1,1,1,2,'committed','{}',1)`, digest, digest, digest); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`INSERT INTO workboard_events
		(id,board_id,sequence,operation_id,kind,actor_id,actor_type,created_at,body)
		VALUES('event','board',1,'operation','board.created','operator','operator',1,'{}')`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestWorkboardMigrationCreatesDurableBoundedSchema(t *testing.T) {
	ctx := context.Background()
	digest := strings.Repeat("a", 64)
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Append(ctx, 0, event("workboard-task", 1, runtime.TaskStarted)); err != nil {
		t.Fatal(err)
	}
	insertWorkboardFixture(t, store.db)
	var version, tables, triggers int
	if err = store.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != currentStorageSchema {
		t.Fatal("schema version", version, err)
	}
	if err = store.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name LIKE 'workboard_%'`).Scan(&tables); err != nil || tables != len(workboardTables) {
		t.Fatal("workboard tables", tables, err)
	}
	if err = store.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name LIKE 'workboard_%_limit'`).Scan(&triggers); err != nil || triggers != 11 {
		t.Fatal("workboard limit triggers", triggers, err)
	}
	if _, err = store.db.Exec(`INSERT INTO workboard_columns(board_id,version,id,state,ordinal,rank,title) VALUES('board',1,'ready','ready',2,'x','Ready')`); err == nil {
		t.Fatal("canonical column identity/order constraint bypassed")
	}
	if _, err = store.db.Exec(`INSERT INTO workboard_cards
		(id,board_id,revision,criteria_revision,state,rank,title,description,priority,remaining_dependencies,attempt_count,attempt_limit,time_limit_ms,token_limit,cost_micros,cancel_requested,pause_requested,created_at,updated_at,body)
		VALUES('invalid','board',1,1,'invented','a','Card','','normal',0,0,3,1,1,1,0,0,1,1,'{}')`); err == nil {
		t.Fatal("card state constraint bypassed")
	}
	if _, err = store.db.Exec(`INSERT INTO workboard_cards
		(id,board_id,revision,criteria_revision,state,rank,title,description,priority,remaining_dependencies,attempt_count,attempt_limit,time_limit_ms,token_limit,cost_micros,cancel_requested,pause_requested,created_at,updated_at,body)
		VALUES('bad-budget','board',1,1,'ready','z','Card','','normal',0,0,0,1,1,1,0,0,1,1,'{}')`); err == nil {
		t.Fatal("invalid normalized card budget accepted")
	}
	if _, err = store.db.Exec(`INSERT INTO workboard_cards
		(id,board_id,revision,criteria_revision,state,rank,title,description,priority,remaining_dependencies,attempt_count,attempt_limit,time_limit_ms,token_limit,cost_micros,cancel_requested,pause_requested,created_at,updated_at,body)
		VALUES('blocked-ready','board',1,1,'ready','z','Card','','normal',1,0,1,0,0,0,0,0,1,1,'{}')`); err == nil {
		t.Fatal("ready card with unresolved dependency count accepted")
	}
	if _, err = store.db.Exec(`INSERT INTO workboard_boards
		(id,revision,layout_revision,event_sequence,graph_revision,graph_digest,state,title,description,card_count,active_claims,created_at,updated_at,body)
		VALUES('archived',1,1,1,1,?,'archived','Archived','',1,1,1,1,'{}')`, strings.Repeat("b", 64)); err == nil {
		t.Fatal("archived board retained an active claim count")
	}
	if _, err = store.db.Exec(`INSERT INTO workboard_operations
		(scope_kind,scope_id,key_digest,operation_id,board_id,request_digest,response_digest,first_sequence,last_sequence,event_count,transaction_bytes,outcome,response,created_at)
		VALUES('creation','session',?,'other-operation','board',?,?,2,2,1,2,'committed','{}',2)`, digest, digest, digest); err == nil {
		t.Fatal("same scoped key digest allocated a second operation")
	}
	if _, err = store.db.Exec(`INSERT INTO workboard_operations
		(scope_kind,scope_id,key_digest,operation_id,board_id,request_digest,response_digest,first_sequence,last_sequence,event_count,transaction_bytes,outcome,response,created_at)
		VALUES('creation','other-session',?,'operation','board',?,?,2,2,1,2,'committed','{}',2)`, strings.Repeat("b", 64), digest, digest); err == nil {
		t.Fatal("server operation ID was reused across scopes")
	}
	if _, err = store.db.Exec(`INSERT INTO workboard_operations
		(scope_kind,scope_id,key_digest,operation_id,board_id,request_digest,response_digest,first_sequence,last_sequence,event_count,transaction_bytes,outcome,response,created_at)
		VALUES('creation','other-session',?,'rejected-operation','board',?,?,2,2,1,2,'rejected','{}',2)`, strings.Repeat("b", 64), digest, digest); err == nil {
		t.Fatal("non-committed workboard receipt accepted")
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	var eventBody, response, cardBody, claimBody []byte
	if err = restarted.db.QueryRow(`SELECT e.body,o.response FROM workboard_events e JOIN workboard_operations o
		ON o.operation_id=e.operation_id
		WHERE e.board_id='board' AND e.sequence=1`).Scan(&eventBody, &response); err != nil || string(eventBody) != "{}" || string(response) != "{}" {
		t.Fatal("restart changed durable workboard records", string(eventBody), string(response), err)
	}
	var columns, cards, dependencies, criteria, attempts, claims, heartbeats, checkpoints int
	if err = restarted.db.QueryRow(`SELECT
		(SELECT count(*) FROM workboard_columns WHERE board_id='board'),
		(SELECT count(*) FROM workboard_cards WHERE board_id='board'),
		(SELECT count(*) FROM workboard_dependencies WHERE board_id='board'),
		(SELECT count(*) FROM workboard_criteria WHERE board_id='board'),
		(SELECT count(*) FROM workboard_attempts WHERE board_id='board'),
		(SELECT count(*) FROM workboard_claims WHERE board_id='board'),
		(SELECT count(*) FROM workboard_claim_heartbeats WHERE claim_id='claim'),
		(SELECT count(*) FROM workboard_checkpoints WHERE attempt_id='attempt')`).
		Scan(&columns, &cards, &dependencies, &criteria, &attempts, &claims, &heartbeats, &checkpoints); err != nil ||
		columns != 7 || cards != 2 || dependencies != 1 || criteria != 1 || attempts != 1 || claims != 1 || heartbeats != 1 || checkpoints != 1 {
		t.Fatal("restart lost workboard graph/lifecycle", columns, cards, dependencies, criteria, attempts, claims, heartbeats, checkpoints, err)
	}
	if err = restarted.db.QueryRow(`SELECT c.body,l.body FROM workboard_cards c JOIN workboard_claims l
		ON l.board_id=c.board_id AND l.card_id=c.id WHERE c.id='card' AND c.current_attempt_id=l.attempt_id AND c.current_claim_id=l.id`).
		Scan(&cardBody, &claimBody); err != nil || string(cardBody) != `{"pause_requested":false}` || string(claimBody) != "{}" {
		t.Fatal("restart changed lifecycle bindings/bodies", string(cardBody), string(claimBody), err)
	}
	var assignee string
	var attemptLimit int
	var timeLimit, tokenLimit, costMicros int64
	if err = restarted.db.QueryRow(`SELECT assignee_id,attempt_limit,time_limit_ms,token_limit,cost_micros
		FROM workboard_cards WHERE board_id='board' AND id='card'`).Scan(&assignee, &attemptLimit, &timeLimit, &tokenLimit, &costMicros); err != nil ||
		assignee != "worker" || attemptLimit != 3 || timeLimit != 60000 || tokenLimit != 1000 || costMicros != 1000 {
		t.Fatal("restart changed normalized ownership/budget", assignee, attemptLimit, timeLimit, tokenLimit, costMicros, err)
	}
}

func TestWorkboardMigrationLifecycleBindings(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	digest := strings.Repeat("a", 64)
	if _, err = store.db.Exec(`INSERT INTO workboard_boards
		(id,revision,layout_revision,event_sequence,graph_revision,graph_digest,state,title,description,card_count,active_claims,created_at,updated_at,body)
		VALUES('board',1,1,1,1,?,'active','Board','',2,1,1,1,'{}')`, digest); err != nil {
		t.Fatal(err)
	}
	longTaskID := strings.Repeat("x", 129)
	if _, err = store.db.Exec(`INSERT INTO task_heads(task_id,session_id,sequence,state) VALUES('linked-task','session',0,'running')`); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`INSERT INTO task_heads(task_id,session_id,sequence,state) VALUES(?,'session',0,'running')`, longTaskID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"card", "other"} {
		if _, err = store.db.Exec(`INSERT INTO workboard_cards
			(id,board_id,revision,criteria_revision,state,rank,title,description,priority,remaining_dependencies,attempt_count,attempt_limit,time_limit_ms,token_limit,cost_micros,cancel_requested,pause_requested,created_at,updated_at,body)
			VALUES(?,'board',1,1,'ready',?,?,'','normal',0,0,3,0,0,0,0,0,1,1,'{}')`, id, id, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = store.db.Exec(`INSERT INTO workboard_criteria
		(board_id,card_id,criteria_revision,ordinal,id,kind,required_source,validator_id,description,required,body)
		VALUES('board','card',1,0,'criterion','objective','deterministic','validator','Pass tests',1,'{}')`); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`INSERT INTO workboard_criteria
		(board_id,card_id,criteria_revision,ordinal,id,kind,required_source,validator_id,description,required,body)
		VALUES('board','card',1,0,'other-criterion','objective','deterministic','validator','No duplicate order',1,'{}')`); err == nil {
		t.Fatal("criteria order was not frozen uniquely")
	}
	if _, err = store.db.Exec(`INSERT INTO workboard_attempts
		(id,board_id,card_id,ordinal,revision,state,worker_id,criteria_revision,criteria_digest,policy_digest,attempt_limit,time_limit_ms,token_limit,cost_micros,started_at,body)
		VALUES('attempt','board','card',1,1,'running','worker',1,?,?,3,0,0,0,1,'{}')`, digest, digest); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`INSERT INTO workboard_claims
		(id,board_id,card_id,attempt_id,revision,state,owner_id,owner_type,expires_at,last_heartbeat,body)
		VALUES('invalid-claim','board','card','attempt',1,'active','worker','worker',1,1,'{}')`); err == nil {
		t.Fatal("non-forward claim expiry accepted")
	}
	if _, err = store.db.Exec(`INSERT INTO workboard_claims
		(id,board_id,card_id,attempt_id,revision,state,owner_id,owner_type,expires_at,last_heartbeat,body)
		VALUES('long-claim','board','card','attempt',1,'active','worker','worker',600000000002,1,'{}')`); err == nil {
		t.Fatal("claim above maximum lease TTL accepted")
	}
	if _, err = store.db.Exec(`INSERT INTO workboard_claims
		(id,board_id,card_id,attempt_id,revision,state,owner_id,owner_type,task_id,expires_at,last_heartbeat,body)
		VALUES('missing-task-claim','board','card','attempt',1,'active','worker','worker','missing-task',2,1,'{}')`); err == nil {
		t.Fatal("claim linked a missing runtime task")
	}
	if _, err = store.db.Exec(`INSERT INTO workboard_claims
		(id,board_id,card_id,attempt_id,revision,state,owner_id,owner_type,task_id,expires_at,last_heartbeat,body)
		VALUES('long-task-claim','board','card','attempt',1,'active','worker','worker',?,2,1,'{}')`, longTaskID); err == nil {
		t.Fatal("claim accepted task ID above 128 bytes")
	}
	if _, err = store.db.Exec(`INSERT INTO workboard_claims
		(id,board_id,card_id,attempt_id,revision,state,owner_id,owner_type,task_id,expires_at,last_heartbeat,body)
		VALUES('claim','board','card','attempt',1,'active','worker','worker','linked-task',2,1,'{}')`); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`INSERT INTO workboard_recovery_proofs
		(id,board_id,card_id,attempt_id,claim_id,task_head_digest,process_proof_digest,effect_evidence_digest,effect_resolution,created_at,body)
		VALUES('proof','board','card','attempt','claim',?,?,?,'effect_free',2,'{}')`, digest, digest, digest); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`INSERT INTO workboard_claim_heartbeats(claim_id,revision,observed_at,expires_at,actor_id,body)
		VALUES('claim',1,1,600000000002,'worker','{}')`); err == nil {
		t.Fatal("heartbeat above maximum lease TTL accepted")
	}
	if _, err = store.db.Exec(`INSERT INTO workboard_claim_heartbeats(claim_id,revision,observed_at,expires_at,actor_id,body)
		VALUES('claim',1,1,2,'worker','{}')`); err != nil {
		t.Fatal(err)
	}
	label64 := strings.Repeat("é", 32)
	if _, err = store.db.Exec(`INSERT INTO workboard_card_labels(board_id,card_id,ordinal,label,label_key)
		VALUES('board','card',0,?,?)`, label64, label64); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`INSERT INTO workboard_card_labels(board_id,card_id,ordinal,label,label_key)
		VALUES('board','card',1,?,?)`, strings.Repeat("é", 33), "other"); err == nil {
		t.Fatal("label above 64 UTF-8 bytes accepted")
	}
	if _, err = store.db.Exec(`INSERT INTO workboard_card_labels(board_id,card_id,ordinal,label,label_key)
		VALUES('board','card',1,'other',?)`, strings.Repeat("é", 33)); err == nil {
		t.Fatal("label key above 64 UTF-8 bytes accepted")
	}
	if _, err = store.db.Exec(`INSERT INTO workboard_attempt_sessions(board_id,card_id,attempt_id,ordinal,session_id)
		VALUES('board','card','attempt',0,'session')`); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`INSERT INTO workboard_attempt_tasks(board_id,card_id,attempt_id,ordinal,task_id)
		VALUES('board','card','attempt',0,'linked-task')`); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`INSERT INTO workboard_checkpoints
		(id,board_id,card_id,attempt_id,claim_id,revision,claim_revision,criteria_revision,criteria_digest,policy_digest,evidence,evidence_digest,actor_id,actor_type,created_at,body)
		VALUES('checkpoint','board','card','attempt','claim',1,1,1,?,?,?,?,'worker','worker',2,'{}')`, digest, digest, "progress", digest); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`INSERT INTO workboard_checkpoints
		(id,board_id,card_id,attempt_id,claim_id,revision,claim_revision,criteria_revision,criteria_digest,policy_digest,evidence,evidence_digest,actor_id,actor_type,created_at,body)
		VALUES('large-checkpoint','board','card','attempt','claim',2,1,1,?,?,?,?,'worker','worker',2,'{}')`, digest, digest, strings.Repeat("é", 32769), digest); err == nil {
		t.Fatal("checkpoint evidence above 64 KiB accepted")
	}
	if _, err = store.db.Exec(`INSERT INTO workboard_checkpoints
		(id,board_id,card_id,attempt_id,claim_id,revision,claim_revision,criteria_revision,criteria_digest,policy_digest,evidence,evidence_digest,actor_id,actor_type,created_at,body)
		VALUES('overflow-checkpoint','board','card','attempt','claim',10001,1,1,?,?,'progress',?,'worker','worker',2,'{}')`, digest, digest, digest); err == nil {
		t.Fatal("checkpoint count/revision bound bypassed")
	}
	if _, err = store.db.Exec(`INSERT INTO workboard_candidates
		(id,board_id,card_id,attempt_id,revision,digest,criteria_digest,policy_digest,evidence_digest,evidence_count,submitted_by,created_at,body)
		VALUES('candidate','board','card','attempt',1,?,?,?,?,0,'worker',2,'{}')`, digest, digest, digest, digest); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`INSERT INTO workboard_candidate_artifacts(board_id,card_id,attempt_id,candidate_id,ordinal,artifact_ref)
		VALUES('board','card','attempt','candidate',0,'artifact')`); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`INSERT INTO workboard_recoveries
		(id,proof_id,board_id,card_id,attempt_id,old_claim_id,old_claim_revision,card_revision,first_sequence,last_sequence,recovered_at,body)
		VALUES('recovery','proof','board','other','attempt','claim',1,2,1,1,2,'{}')`); err == nil {
		t.Fatal("recovery receipt cross-linked another card")
	}
	for _, table := range []string{"workboard_card_labels", "workboard_attempt_tasks", "workboard_attempt_sessions", "workboard_candidate_artifacts"} {
		var present int
		if err = store.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&present); err != nil || present != 1 {
			t.Fatal("missing normalized bounded collection", table, present, err)
		}
	}
}

func TestWorkboardMigrationRejectsPartialSchemaAndRollsBack(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	downgradeWorkboards(t, store.db)
	if _, err = store.db.Exec(`CREATE TABLE workboard_cards(sentinel TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, openErr := Open(ctx, path); openErr == nil {
		reopened.Close()
		t.Fatal("partial schema accepted")
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var version, sentinel, rolledBack int
	if err = raw.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 34 {
		t.Fatal("failed migration advanced schema", version, err)
	}
	if err = raw.QueryRow(`SELECT count(*) FROM pragma_table_info('workboard_cards') WHERE name='sentinel'`).Scan(&sentinel); err != nil || sentinel != 1 {
		t.Fatal("partial source table changed", sentinel, err)
	}
	if err = raw.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='workboard_boards'`).Scan(&rolledBack); err != nil || rolledBack != 0 {
		t.Fatal("failed migration left partial objects", rolledBack, err)
	}
}

func TestWorkboardMigrationRejectsForgedRetainedObject(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`DROP TRIGGER workboard_label_limit;
		CREATE TRIGGER workboard_label_limit BEFORE INSERT ON workboard_card_labels BEGIN SELECT 1; END;
		DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_delete; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_update; DROP INDEX IF EXISTS workboard_task_start_claims_board; DROP TABLE IF EXISTS workboard_task_start_claims; PRAGMA user_version=34`); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, openErr := Open(ctx, path); openErr == nil {
		reopened.Close()
		t.Fatal("forged retained trigger accepted")
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var version int
	var definition string
	if err = raw.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 34 {
		t.Fatal("failed retained-schema validation advanced version", version, err)
	}
	if err = raw.QueryRow(`SELECT sql FROM sqlite_master WHERE type='trigger' AND name='workboard_label_limit'`).Scan(&definition); err != nil || !strings.Contains(definition, "SELECT 1") {
		t.Fatal("failed retained-schema validation changed trigger", definition, err)
	}
}

func TestWorkboardMigrationConcurrentOpenSerializes(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	downgradeWorkboards(t, store.db)
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			<-start
			opened, openErr := Open(ctx, path)
			if openErr == nil {
				openErr = opened.Close()
			}
			errs <- openErr
		})
	}
	close(start)
	wg.Wait()
	close(errs)
	for err = range errs {
		if err != nil {
			t.Fatal("serialized open", err)
		}
	}
	verified, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer verified.Close()
	var version int
	if err = verified.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != currentStorageSchema {
		t.Fatal(version, err)
	}
}
