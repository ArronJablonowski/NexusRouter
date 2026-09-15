package telemetry

import (
	"context"
	"database/sql"
	"strings"
)

// Schema 35 reserves normalized, bounded storage for the native workboard. A
// database with no retained workboard objects is created directly at the latest
// schema-36 shape; only retained exact schema-35 objects take the compatibility
// rebuild. The command service remains responsible for graph-cycle/depth
// preflight and for keeping indexed projections identical to canonical bodies.
func migrateWorkboards(ctx context.Context, conn *sql.Conn) error {
	var existing int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master
		WHERE type IN('table','index','trigger') AND name GLOB 'workboard_*'`).Scan(&existing); err != nil {
		return err
	}
	// Tests and supported recovery rehearsals may lower user_version while
	// retaining later objects. Accept only the complete, exact schema; any
	// partial or forged object set fails without attempting repair.
	if existing > 0 {
		if err := validateWorkboardSchema(ctx, conn); err == nil {
			_, err = conn.ExecContext(ctx, "PRAGMA user_version=35")
			return err
		}
		// A deliberately lowered user_version may retain the complete later
		// schema. It is safe to advance only after that exact shape validates.
		if err := validateWorkboardSchema36(ctx, conn); err != nil {
			// A complete schema-40 workboard may be retained while a recovery
			// rehearsal lowers user_version. Validate it exactly, then resume at
			// 36 so non-workboard migrations still run in order.
			if err = validateWorkboardSchema40(ctx, conn); err != nil {
				return err
			}
		}
		_, err := conn.ExecContext(ctx, "PRAGMA user_version=36")
		return err
	}
	_, err := conn.ExecContext(ctx, `CREATE TABLE workboard_boards(
	 id TEXT PRIMARY KEY CHECK(length(CAST(id AS BLOB)) BETWEEN 1 AND 128),
	 revision INTEGER NOT NULL CHECK(revision>0),
	 layout_revision INTEGER NOT NULL CHECK(layout_revision>0),
	 event_sequence INTEGER NOT NULL CHECK(event_sequence>0),
	 graph_revision INTEGER NOT NULL CHECK(graph_revision>0),
	 graph_digest TEXT NOT NULL CHECK(length(graph_digest)=64 AND graph_digest NOT GLOB '*[^0-9a-f]*'),
	 state TEXT NOT NULL CHECK(state IN('active','archived')),
	 title TEXT NOT NULL CHECK(length(CAST(title AS BLOB)) BETWEEN 1 AND 256),
	 description TEXT NOT NULL CHECK(length(CAST(description AS BLOB))<=65536),
	 card_count INTEGER NOT NULL CHECK(card_count BETWEEN 0 AND 10000),
	 active_claims INTEGER NOT NULL CHECK(active_claims BETWEEN 0 AND card_count),
	 created_at INTEGER NOT NULL CHECK(created_at>=0),
	 updated_at INTEGER NOT NULL CHECK(updated_at>=created_at),
	 body BLOB NOT NULL CHECK(length(body) BETWEEN 1 AND 1048576),
	 CHECK(state!='archived' OR active_claims=0));

	CREATE TABLE workboard_columns(
	 board_id TEXT NOT NULL REFERENCES workboard_boards(id) ON DELETE CASCADE,
	 version INTEGER NOT NULL CHECK(version=1),
	 id TEXT NOT NULL CHECK(id=state),
	 state TEXT NOT NULL CHECK(state IN('backlog','ready','in_progress','blocked','review','done','canceled')),
	 ordinal INTEGER NOT NULL CHECK(ordinal BETWEEN 0 AND 6),
	 rank TEXT NOT NULL CHECK(length(CAST(rank AS BLOB)) BETWEEN 1 AND 128),
	 title TEXT NOT NULL CHECK(length(CAST(title AS BLOB)) BETWEEN 1 AND 256),
	 PRIMARY KEY(board_id,state), UNIQUE(board_id,id), UNIQUE(board_id,ordinal), UNIQUE(board_id,rank),
	 CHECK((state='backlog' AND ordinal=0 AND title='Backlog') OR
	       (state='ready' AND ordinal=1 AND title='Ready') OR
	       (state='in_progress' AND ordinal=2 AND title='In Progress') OR
	       (state='blocked' AND ordinal=3 AND title='Blocked') OR
	       (state='review' AND ordinal=4 AND title='Review') OR
	       (state='done' AND ordinal=5 AND title='Done') OR
	       (state='canceled' AND ordinal=6 AND title='Canceled')));

	CREATE TABLE workboard_cards(
	 id TEXT PRIMARY KEY CHECK(length(CAST(id AS BLOB)) BETWEEN 1 AND 128),
	 board_id TEXT NOT NULL REFERENCES workboard_boards(id) ON DELETE CASCADE,
	 revision INTEGER NOT NULL CHECK(revision>0),
	 criteria_revision INTEGER NOT NULL CHECK(criteria_revision>0),
	 state TEXT NOT NULL CHECK(state IN('backlog','ready','in_progress','blocked','review','done','canceled')),
	 rank TEXT NOT NULL CHECK(length(CAST(rank AS BLOB)) BETWEEN 1 AND 128),
	 title TEXT NOT NULL CHECK(length(CAST(title AS BLOB)) BETWEEN 1 AND 256),
	 description TEXT NOT NULL CHECK(length(CAST(description AS BLOB))<=65536),
	 priority TEXT NOT NULL CHECK(priority IN('urgent','high','normal','low')),
	 parent_id TEXT,
	 assignee_id TEXT CHECK(length(CAST(assignee_id AS BLOB)) BETWEEN 1 AND 128),
	 block_reason TEXT CHECK(length(CAST(block_reason AS BLOB)) BETWEEN 1 AND 128),
	 remaining_dependencies INTEGER NOT NULL CHECK(remaining_dependencies BETWEEN 0 AND 64),
	 attempt_count INTEGER NOT NULL CHECK(attempt_count BETWEEN 0 AND 32),
	 attempt_limit INTEGER NOT NULL CHECK(attempt_limit BETWEEN 1 AND 32),
	 time_limit_ms INTEGER NOT NULL CHECK(time_limit_ms BETWEEN 0 AND 2592000000),
	 token_limit INTEGER NOT NULL CHECK(token_limit BETWEEN 0 AND 1000000000),
	 cost_micros INTEGER NOT NULL CHECK(cost_micros BETWEEN 0 AND 1000000000000),
	 current_attempt_id TEXT,
	 current_claim_id TEXT,
	 acceptance_id TEXT,
	 cancel_requested INTEGER NOT NULL CHECK(cancel_requested IN(0,1)),
	 pause_requested INTEGER NOT NULL CHECK(pause_requested IN(0,1)),
	 created_at INTEGER NOT NULL CHECK(created_at>=0),
	 updated_at INTEGER NOT NULL CHECK(updated_at>=created_at),
	 body BLOB NOT NULL CHECK(length(body) BETWEEN 1 AND 1048576),
	 UNIQUE(board_id,id), UNIQUE(board_id,state,rank),
	 FOREIGN KEY(board_id,parent_id) REFERENCES workboard_cards(board_id,id),
	 FOREIGN KEY(board_id,id,current_attempt_id) REFERENCES workboard_attempts(board_id,card_id,id) DEFERRABLE INITIALLY DEFERRED,
	 FOREIGN KEY(board_id,id,current_claim_id) REFERENCES workboard_claims(board_id,card_id,id) DEFERRABLE INITIALLY DEFERRED,
	 FOREIGN KEY(board_id,id,acceptance_id) REFERENCES workboard_acceptances(board_id,card_id,id) DEFERRABLE INITIALLY DEFERRED,
	 CHECK(parent_id IS NULL OR parent_id!=id),
	 CHECK(state!='ready' OR remaining_dependencies=0),
	 CHECK((state='blocked' AND block_reason IS NOT NULL) OR (state!='blocked' AND block_reason IS NULL)),
	 CHECK((state IN('in_progress','blocked') AND current_attempt_id IS NOT NULL AND current_claim_id IS NOT NULL AND acceptance_id IS NULL) OR
	       (state='review' AND current_attempt_id IS NOT NULL AND current_claim_id IS NULL AND acceptance_id IS NULL) OR
	       (state='done' AND current_attempt_id IS NOT NULL AND current_claim_id IS NULL AND acceptance_id IS NOT NULL AND remaining_dependencies=0) OR
	       (state IN('backlog','ready','canceled') AND current_claim_id IS NULL AND acceptance_id IS NULL)),
	 CHECK((cancel_requested=0 AND pause_requested=0) OR state IN('in_progress','blocked')));
	CREATE INDEX workboard_cards_board_state_rank ON workboard_cards(board_id,state,rank,id);
	CREATE INDEX workboard_cards_parent ON workboard_cards(board_id,parent_id,id);
	CREATE TABLE workboard_card_labels(
	 board_id TEXT NOT NULL,
	 card_id TEXT NOT NULL,
	 ordinal INTEGER NOT NULL CHECK(ordinal BETWEEN 0 AND 31),
	 label TEXT NOT NULL CHECK(length(CAST(label AS BLOB)) BETWEEN 1 AND 64),
	 label_key TEXT NOT NULL CHECK(length(CAST(label_key AS BLOB)) BETWEEN 1 AND 64),
	 PRIMARY KEY(board_id,card_id,ordinal), UNIQUE(board_id,card_id,label_key),
	 FOREIGN KEY(board_id,card_id) REFERENCES workboard_cards(board_id,id) ON DELETE CASCADE);

	CREATE TABLE workboard_dependencies(
	 board_id TEXT NOT NULL,
	 card_id TEXT NOT NULL,
	 dependency_id TEXT NOT NULL,
	 created_sequence INTEGER NOT NULL CHECK(created_sequence>0),
	 PRIMARY KEY(board_id,card_id,dependency_id),
	 FOREIGN KEY(board_id,card_id) REFERENCES workboard_cards(board_id,id) ON DELETE CASCADE,
	 FOREIGN KEY(board_id,dependency_id) REFERENCES workboard_cards(board_id,id),
	 CHECK(card_id!=dependency_id));
	CREATE INDEX workboard_dependencies_reverse ON workboard_dependencies(board_id,dependency_id,card_id);

	CREATE TABLE workboard_attempts(
	 id TEXT PRIMARY KEY CHECK(length(CAST(id AS BLOB)) BETWEEN 1 AND 128),
	 board_id TEXT NOT NULL,
	 card_id TEXT NOT NULL,
	 ordinal INTEGER NOT NULL CHECK(ordinal BETWEEN 1 AND 32),
	 revision INTEGER NOT NULL CHECK(revision>0),
	 state TEXT NOT NULL CHECK(state IN('running','review','accepted','rejected','failed','canceled')),
	 worker_id TEXT NOT NULL CHECK(length(CAST(worker_id AS BLOB)) BETWEEN 1 AND 128),
	 criteria_revision INTEGER NOT NULL CHECK(criteria_revision>0),
	 criteria_digest TEXT NOT NULL CHECK(length(criteria_digest)=64 AND criteria_digest NOT GLOB '*[^0-9a-f]*'),
	 policy_digest TEXT NOT NULL CHECK(length(policy_digest)=64 AND policy_digest NOT GLOB '*[^0-9a-f]*'),
	 attempt_limit INTEGER NOT NULL CHECK(attempt_limit BETWEEN 1 AND 32),
	 time_limit_ms INTEGER NOT NULL CHECK(time_limit_ms BETWEEN 0 AND 2592000000),
	 token_limit INTEGER NOT NULL CHECK(token_limit BETWEEN 0 AND 1000000000),
	 cost_micros INTEGER NOT NULL CHECK(cost_micros BETWEEN 0 AND 1000000000000),
	 started_at INTEGER NOT NULL CHECK(started_at>=0),
	 ended_at INTEGER CHECK(ended_at>=started_at),
	 body BLOB NOT NULL CHECK(length(body) BETWEEN 1 AND 1048576),
	 UNIQUE(board_id,card_id,id), UNIQUE(board_id,card_id,ordinal),
	 FOREIGN KEY(board_id,card_id) REFERENCES workboard_cards(board_id,id) ON DELETE CASCADE,
	 CHECK((state='running' AND ended_at IS NULL) OR (state!='running' AND ended_at IS NOT NULL)));
	CREATE UNIQUE INDEX workboard_attempts_active ON workboard_attempts(board_id,card_id) WHERE state IN('running','review');
	CREATE TABLE workboard_attempt_tasks(
	 board_id TEXT NOT NULL,
	 card_id TEXT NOT NULL,
	 attempt_id TEXT NOT NULL,
	 ordinal INTEGER NOT NULL CHECK(ordinal BETWEEN 0 AND 127),
	 task_id TEXT NOT NULL REFERENCES task_heads(task_id),
	 PRIMARY KEY(board_id,card_id,attempt_id,ordinal), UNIQUE(board_id,card_id,attempt_id,task_id),
	 FOREIGN KEY(board_id,card_id,attempt_id) REFERENCES workboard_attempts(board_id,card_id,id) ON DELETE CASCADE);
	CREATE TABLE workboard_attempt_sessions(
	 board_id TEXT NOT NULL,
	 card_id TEXT NOT NULL,
	 attempt_id TEXT NOT NULL,
	 ordinal INTEGER NOT NULL CHECK(ordinal BETWEEN 0 AND 127),
	 session_id TEXT NOT NULL CHECK(length(CAST(session_id AS BLOB)) BETWEEN 1 AND 128),
	 PRIMARY KEY(board_id,card_id,attempt_id,ordinal), UNIQUE(board_id,card_id,attempt_id,session_id),
	 FOREIGN KEY(board_id,card_id,attempt_id) REFERENCES workboard_attempts(board_id,card_id,id) ON DELETE CASCADE);

	CREATE TABLE workboard_criteria(
	 board_id TEXT NOT NULL,
	 card_id TEXT NOT NULL,
	 criteria_revision INTEGER NOT NULL CHECK(criteria_revision>0),
	 ordinal INTEGER NOT NULL CHECK(ordinal BETWEEN 0 AND 31),
	 id TEXT NOT NULL CHECK(length(CAST(id AS BLOB)) BETWEEN 1 AND 128),
	 kind TEXT NOT NULL CHECK(kind IN('objective','subjective')),
	 required_source TEXT NOT NULL CHECK(required_source IN('deterministic','user_feedback')),
	 validator_id TEXT NOT NULL CHECK(length(CAST(validator_id AS BLOB)) BETWEEN 1 AND 128),
	 description TEXT NOT NULL CHECK(length(CAST(description AS BLOB)) BETWEEN 1 AND 4096),
	 required INTEGER NOT NULL CHECK(required IN(0,1)),
	 body BLOB NOT NULL CHECK(length(body) BETWEEN 1 AND 65536),
	 PRIMARY KEY(board_id,card_id,criteria_revision,id), UNIQUE(board_id,card_id,criteria_revision,ordinal),
	 FOREIGN KEY(board_id,card_id) REFERENCES workboard_cards(board_id,id) ON DELETE CASCADE,
	 CHECK((kind='objective' AND required_source='deterministic') OR (kind='subjective' AND required_source='user_feedback')));

	CREATE TABLE workboard_claims(
	 id TEXT PRIMARY KEY CHECK(length(CAST(id AS BLOB)) BETWEEN 1 AND 128),
	 board_id TEXT NOT NULL,
	 card_id TEXT NOT NULL,
	 attempt_id TEXT NOT NULL,
	 revision INTEGER NOT NULL CHECK(revision>0),
	 state TEXT NOT NULL CHECK(state IN('active','attention','released')),
	 owner_id TEXT NOT NULL CHECK(length(CAST(owner_id AS BLOB)) BETWEEN 1 AND 128),
	 owner_type TEXT NOT NULL CHECK(owner_type='worker'),
	 task_id TEXT REFERENCES task_heads(task_id) CHECK(task_id IS NULL OR length(CAST(task_id AS BLOB)) BETWEEN 1 AND 128),
	 expires_at INTEGER NOT NULL CHECK(expires_at>=0),
	 last_heartbeat INTEGER NOT NULL CHECK(last_heartbeat>=0 AND expires_at-last_heartbeat BETWEEN 1 AND 600000000000),
	 released_at INTEGER CHECK(released_at>=last_heartbeat),
	 body BLOB NOT NULL CHECK(length(body) BETWEEN 1 AND 16384),
	 UNIQUE(board_id,card_id,attempt_id), UNIQUE(board_id,card_id,id), UNIQUE(board_id,card_id,attempt_id,id),
	 FOREIGN KEY(board_id,card_id,attempt_id) REFERENCES workboard_attempts(board_id,card_id,id) ON DELETE CASCADE,
	 CHECK((state IN('active','attention') AND released_at IS NULL) OR (state='released' AND released_at IS NOT NULL)));
	CREATE UNIQUE INDEX workboard_claims_active ON workboard_claims(board_id,card_id) WHERE state IN('active','attention');
	CREATE INDEX workboard_claims_expiry ON workboard_claims(state,expires_at,id);

	CREATE TABLE workboard_claim_heartbeats(
	 claim_id TEXT NOT NULL REFERENCES workboard_claims(id) ON DELETE CASCADE,
	 revision INTEGER NOT NULL CHECK(revision>0),
	 observed_at INTEGER NOT NULL CHECK(observed_at>=0),
	 expires_at INTEGER NOT NULL CHECK(expires_at-observed_at BETWEEN 1 AND 600000000000),
	 actor_id TEXT NOT NULL CHECK(length(CAST(actor_id AS BLOB)) BETWEEN 1 AND 128),
	 body BLOB NOT NULL CHECK(length(body) BETWEEN 1 AND 16384),
	 PRIMARY KEY(claim_id,revision));

	CREATE TABLE workboard_checkpoints(
	 id TEXT PRIMARY KEY CHECK(length(CAST(id AS BLOB)) BETWEEN 1 AND 128),
	 board_id TEXT NOT NULL,
	 card_id TEXT NOT NULL,
	 attempt_id TEXT NOT NULL,
	 claim_id TEXT NOT NULL,
	 revision INTEGER NOT NULL CHECK(revision BETWEEN 1 AND 10000),
	 claim_revision INTEGER NOT NULL CHECK(claim_revision>0),
	 criteria_revision INTEGER NOT NULL CHECK(criteria_revision>0),
	 criteria_digest TEXT NOT NULL CHECK(length(criteria_digest)=64 AND criteria_digest NOT GLOB '*[^0-9a-f]*'),
	 policy_digest TEXT NOT NULL CHECK(length(policy_digest)=64 AND policy_digest NOT GLOB '*[^0-9a-f]*'),
	 evidence TEXT NOT NULL CHECK(length(CAST(evidence AS BLOB)) BETWEEN 1 AND 65536),
	 evidence_digest TEXT NOT NULL CHECK(length(evidence_digest)=64 AND evidence_digest NOT GLOB '*[^0-9a-f]*'),
	 actor_id TEXT NOT NULL CHECK(length(CAST(actor_id AS BLOB)) BETWEEN 1 AND 128),
	 actor_type TEXT NOT NULL CHECK(actor_type='worker'),
	 created_at INTEGER NOT NULL CHECK(created_at>=0),
	 body BLOB NOT NULL CHECK(length(body) BETWEEN 1 AND 1048576),
	 UNIQUE(board_id,card_id,attempt_id,revision),
	 FOREIGN KEY(board_id,card_id,attempt_id,claim_id)
	  REFERENCES workboard_claims(board_id,card_id,attempt_id,id));
	CREATE INDEX workboard_checkpoints_attempt ON workboard_checkpoints(board_id,card_id,attempt_id,revision);

	CREATE TABLE workboard_candidates(
	 id TEXT PRIMARY KEY CHECK(length(CAST(id AS BLOB)) BETWEEN 1 AND 128),
	 board_id TEXT NOT NULL,
	 card_id TEXT NOT NULL,
	 attempt_id TEXT NOT NULL,
	 revision INTEGER NOT NULL CHECK(revision>0),
	 digest TEXT NOT NULL CHECK(length(digest)=64 AND digest NOT GLOB '*[^0-9a-f]*'),
	 criteria_digest TEXT NOT NULL CHECK(length(criteria_digest)=64 AND criteria_digest NOT GLOB '*[^0-9a-f]*'),
	 policy_digest TEXT NOT NULL CHECK(length(policy_digest)=64 AND policy_digest NOT GLOB '*[^0-9a-f]*'),
	 evidence_digest TEXT NOT NULL CHECK(length(evidence_digest)=64 AND evidence_digest NOT GLOB '*[^0-9a-f]*'),
	 evidence_count INTEGER NOT NULL CHECK(evidence_count BETWEEN 0 AND 100),
	 submitted_by TEXT NOT NULL CHECK(length(CAST(submitted_by AS BLOB)) BETWEEN 1 AND 128),
	 created_at INTEGER NOT NULL CHECK(created_at>=0),
	 body BLOB NOT NULL CHECK(length(body) BETWEEN 1 AND 1048576),
	 UNIQUE(board_id,card_id,attempt_id), UNIQUE(board_id,card_id,attempt_id,id),
	 FOREIGN KEY(board_id,card_id,attempt_id) REFERENCES workboard_attempts(board_id,card_id,id) ON DELETE CASCADE);
	CREATE TABLE workboard_candidate_artifacts(
	 board_id TEXT NOT NULL,
	 card_id TEXT NOT NULL,
	 attempt_id TEXT NOT NULL,
	 candidate_id TEXT NOT NULL,
	 ordinal INTEGER NOT NULL CHECK(ordinal BETWEEN 0 AND 31),
	 artifact_ref TEXT NOT NULL CHECK(length(CAST(artifact_ref AS BLOB)) BETWEEN 1 AND 128),
	 PRIMARY KEY(board_id,card_id,attempt_id,candidate_id,ordinal),
	 UNIQUE(board_id,card_id,attempt_id,candidate_id,artifact_ref),
	 FOREIGN KEY(board_id,card_id,attempt_id,candidate_id)
	  REFERENCES workboard_candidates(board_id,card_id,attempt_id,id) ON DELETE CASCADE);

	CREATE TABLE workboard_evidence(
	 id TEXT PRIMARY KEY CHECK(length(CAST(id AS BLOB)) BETWEEN 1 AND 128),
	 board_id TEXT NOT NULL,
	 card_id TEXT NOT NULL,
	 attempt_id TEXT NOT NULL,
	 candidate_id TEXT NOT NULL,
	 criterion_id TEXT NOT NULL,
	 revision INTEGER NOT NULL CHECK(revision>0),
	 source TEXT NOT NULL CHECK(source IN('deterministic','user_feedback','model_audit')),
	 outcome TEXT NOT NULL CHECK(outcome IN('passed','failed','abstained')),
	 actor_id TEXT NOT NULL CHECK(length(CAST(actor_id AS BLOB)) BETWEEN 1 AND 128),
	 actor_type TEXT NOT NULL CHECK(actor_type IN('operator','validator','model')),
	 reference TEXT NOT NULL CHECK(length(CAST(reference AS BLOB)) BETWEEN 1 AND 128),
	 candidate_digest TEXT NOT NULL CHECK(length(candidate_digest)=64 AND candidate_digest NOT GLOB '*[^0-9a-f]*'),
	 criteria_revision INTEGER NOT NULL CHECK(criteria_revision>0),
	 criteria_digest TEXT NOT NULL CHECK(length(criteria_digest)=64 AND criteria_digest NOT GLOB '*[^0-9a-f]*'),
	 policy_digest TEXT NOT NULL CHECK(length(policy_digest)=64 AND policy_digest NOT GLOB '*[^0-9a-f]*'),
	 created_at INTEGER NOT NULL CHECK(created_at>=0),
	 body BLOB NOT NULL CHECK(length(body) BETWEEN 1 AND 65536),
	 UNIQUE(board_id,card_id,attempt_id,revision),
	 FOREIGN KEY(board_id,card_id,attempt_id,candidate_id) REFERENCES workboard_candidates(board_id,card_id,attempt_id,id) ON DELETE CASCADE,
	 FOREIGN KEY(board_id,card_id,criteria_revision,criterion_id) REFERENCES workboard_criteria(board_id,card_id,criteria_revision,id),
	 CHECK((source='deterministic' AND actor_type='validator' AND outcome IN('passed','failed')) OR
	       (source='user_feedback' AND actor_type='operator' AND outcome IN('passed','failed')) OR
	       (source='model_audit' AND actor_type='model')));
	CREATE INDEX workboard_evidence_attempt ON workboard_evidence(board_id,card_id,attempt_id,revision);

	CREATE TABLE workboard_acceptances(
	 id TEXT PRIMARY KEY CHECK(length(CAST(id AS BLOB)) BETWEEN 1 AND 128),
	 board_id TEXT NOT NULL,
	 card_id TEXT NOT NULL,
	 attempt_id TEXT NOT NULL,
	 candidate_id TEXT NOT NULL,
	 candidate_digest TEXT NOT NULL CHECK(length(candidate_digest)=64 AND candidate_digest NOT GLOB '*[^0-9a-f]*'),
	 criteria_revision INTEGER NOT NULL CHECK(criteria_revision>0),
	 criteria_digest TEXT NOT NULL CHECK(length(criteria_digest)=64 AND criteria_digest NOT GLOB '*[^0-9a-f]*'),
	 evidence_head_revision INTEGER NOT NULL CHECK(evidence_head_revision>0),
	 evidence_set_digest TEXT NOT NULL CHECK(length(evidence_set_digest)=64 AND evidence_set_digest NOT GLOB '*[^0-9a-f]*'),
	 policy_digest TEXT NOT NULL CHECK(length(policy_digest)=64 AND policy_digest NOT GLOB '*[^0-9a-f]*'),
	 decision TEXT NOT NULL CHECK(decision IN('accepted','rejected')),
	 decided_by TEXT NOT NULL CHECK(length(CAST(decided_by AS BLOB)) BETWEEN 1 AND 128),
	 decided_by_type TEXT NOT NULL CHECK(decided_by_type IN('operator','validator')),
	 decision_authority_id TEXT NOT NULL CHECK(length(CAST(decision_authority_id AS BLOB)) BETWEEN 1 AND 128),
	 decided_at INTEGER NOT NULL CHECK(decided_at>=0),
	 body BLOB NOT NULL CHECK(length(body) BETWEEN 1 AND 16384),
	 UNIQUE(board_id,card_id,attempt_id), UNIQUE(board_id,card_id,id),
	 FOREIGN KEY(board_id,card_id,attempt_id,candidate_id) REFERENCES workboard_candidates(board_id,card_id,attempt_id,id));

	CREATE TABLE workboard_recovery_proofs(
	 id TEXT PRIMARY KEY CHECK(length(CAST(id AS BLOB)) BETWEEN 1 AND 128),
	 board_id TEXT NOT NULL,
	 card_id TEXT NOT NULL,
	 attempt_id TEXT NOT NULL,
	 claim_id TEXT NOT NULL REFERENCES workboard_claims(id),
	 task_head_digest TEXT NOT NULL CHECK(length(task_head_digest)=64 AND task_head_digest NOT GLOB '*[^0-9a-f]*'),
	 process_proof_digest TEXT NOT NULL CHECK(length(process_proof_digest)=64 AND process_proof_digest NOT GLOB '*[^0-9a-f]*'),
	 effect_evidence_digest TEXT NOT NULL CHECK(length(effect_evidence_digest)=64 AND effect_evidence_digest NOT GLOB '*[^0-9a-f]*'),
	 effect_resolution TEXT NOT NULL CHECK(effect_resolution IN('effect_free','resolved_no_replay')),
	 created_at INTEGER NOT NULL CHECK(created_at>=0),
	 body BLOB NOT NULL CHECK(length(body) BETWEEN 1 AND 65536),
	 UNIQUE(board_id,card_id,attempt_id,claim_id,id),
	 FOREIGN KEY(board_id,card_id,attempt_id,claim_id) REFERENCES workboard_claims(board_id,card_id,attempt_id,id));

	CREATE TABLE workboard_recoveries(
	 id TEXT PRIMARY KEY CHECK(length(CAST(id AS BLOB)) BETWEEN 1 AND 128),
	 proof_id TEXT NOT NULL UNIQUE,
	 board_id TEXT NOT NULL REFERENCES workboard_boards(id),
	 card_id TEXT NOT NULL REFERENCES workboard_cards(id),
	 attempt_id TEXT NOT NULL REFERENCES workboard_attempts(id),
	 old_claim_id TEXT NOT NULL REFERENCES workboard_claims(id),
	 old_claim_revision INTEGER NOT NULL CHECK(old_claim_revision>0),
	 card_revision INTEGER NOT NULL CHECK(card_revision>0),
	 first_sequence INTEGER NOT NULL CHECK(first_sequence>0),
	 last_sequence INTEGER NOT NULL CHECK(last_sequence>=first_sequence AND last_sequence-first_sequence+1<=128),
	 recovered_at INTEGER NOT NULL CHECK(recovered_at>=0),
	 body BLOB NOT NULL CHECK(length(body) BETWEEN 1 AND 16384),
	 FOREIGN KEY(board_id,card_id,attempt_id,old_claim_id,proof_id)
	  REFERENCES workboard_recovery_proofs(board_id,card_id,attempt_id,claim_id,id));

	CREATE TABLE workboard_operations(
	 scope_kind TEXT NOT NULL CHECK(scope_kind IN('board','creation')),
	 scope_id TEXT NOT NULL CHECK(length(CAST(scope_id AS BLOB)) BETWEEN 1 AND 128),
	 key_digest TEXT NOT NULL CHECK(length(key_digest)=64 AND key_digest NOT GLOB '*[^0-9a-f]*'),
	 operation_id TEXT NOT NULL UNIQUE CHECK(length(CAST(operation_id AS BLOB)) BETWEEN 1 AND 128),
	 board_id TEXT NOT NULL REFERENCES workboard_boards(id),
	 request_digest TEXT NOT NULL CHECK(length(request_digest)=64 AND request_digest NOT GLOB '*[^0-9a-f]*'),
	 response_digest TEXT NOT NULL CHECK(length(response_digest)=64 AND response_digest NOT GLOB '*[^0-9a-f]*'),
	 first_sequence INTEGER NOT NULL CHECK(first_sequence>0),
	 last_sequence INTEGER NOT NULL CHECK(last_sequence>=first_sequence),
	 event_count INTEGER NOT NULL CHECK(event_count BETWEEN 1 AND 128 AND event_count=last_sequence-first_sequence+1),
	 transaction_bytes INTEGER NOT NULL CHECK(transaction_bytes BETWEEN 1 AND 1048576),
	 outcome TEXT NOT NULL CHECK(outcome='committed'),
	 response BLOB NOT NULL CHECK(length(response) BETWEEN 1 AND 16384),
	 created_at INTEGER NOT NULL CHECK(created_at>=0),
	 PRIMARY KEY(scope_kind,scope_id,key_digest), UNIQUE(board_id,operation_id),
	 CHECK(scope_kind!='board' OR scope_id=board_id));
	CREATE INDEX workboard_operations_board_created ON workboard_operations(board_id,created_at,operation_id);

	CREATE TABLE workboard_events(
	 id TEXT NOT NULL UNIQUE CHECK(length(CAST(id AS BLOB)) BETWEEN 1 AND 128),
	 board_id TEXT NOT NULL REFERENCES workboard_boards(id) ON DELETE CASCADE,
	 sequence INTEGER NOT NULL CHECK(sequence>0),
	 operation_id TEXT NOT NULL,
	 kind TEXT NOT NULL CHECK(length(CAST(kind AS BLOB)) BETWEEN 1 AND 128),
	 actor_id TEXT NOT NULL CHECK(length(CAST(actor_id AS BLOB)) BETWEEN 1 AND 128),
	 actor_type TEXT NOT NULL CHECK(actor_type IN('operator','worker','validator','model','system')),
	 card_id TEXT CHECK(length(CAST(card_id AS BLOB)) BETWEEN 1 AND 128),
	 created_at INTEGER NOT NULL CHECK(created_at>=0),
	 body BLOB NOT NULL CHECK(length(body) BETWEEN 1 AND 1048576),
	 PRIMARY KEY(board_id,sequence),
	 FOREIGN KEY(board_id,card_id) REFERENCES workboard_cards(board_id,id),
	 FOREIGN KEY(board_id,operation_id) REFERENCES workboard_operations(board_id,operation_id) DEFERRABLE INITIALLY DEFERRED);
	CREATE INDEX workboard_events_operation ON workboard_events(operation_id,sequence);

	CREATE TRIGGER workboard_board_limit BEFORE INSERT ON workboard_boards
	 WHEN (SELECT count(*) FROM workboard_boards)>=100
	 BEGIN SELECT RAISE(ABORT,'workboard board limit'); END;
	CREATE TRIGGER workboard_card_limit BEFORE INSERT ON workboard_cards
	 WHEN (SELECT count(*) FROM workboard_cards WHERE board_id=NEW.board_id)>=10000
	 BEGIN SELECT RAISE(ABORT,'workboard card limit'); END;
	CREATE TRIGGER workboard_dependency_limit BEFORE INSERT ON workboard_dependencies
	 WHEN (SELECT count(*) FROM workboard_dependencies WHERE board_id=NEW.board_id AND card_id=NEW.card_id)>=64
	 BEGIN SELECT RAISE(ABORT,'workboard dependency limit'); END;
	CREATE TRIGGER workboard_reverse_fanout_limit BEFORE INSERT ON workboard_dependencies
	 WHEN (SELECT count(*) FROM workboard_dependencies WHERE board_id=NEW.board_id AND dependency_id=NEW.dependency_id)>=64
	 BEGIN SELECT RAISE(ABORT,'workboard reverse fanout limit'); END;
	CREATE TRIGGER workboard_criteria_limit BEFORE INSERT ON workboard_criteria
	 WHEN (SELECT count(*) FROM workboard_criteria WHERE board_id=NEW.board_id AND card_id=NEW.card_id AND criteria_revision=NEW.criteria_revision)>=32
	 BEGIN SELECT RAISE(ABORT,'workboard criteria limit'); END;
	CREATE TRIGGER workboard_evidence_limit BEFORE INSERT ON workboard_evidence
	 WHEN (SELECT count(*) FROM workboard_evidence WHERE board_id=NEW.board_id AND card_id=NEW.card_id AND attempt_id=NEW.attempt_id)>=100
	 BEGIN SELECT RAISE(ABORT,'workboard evidence limit'); END;
	CREATE TRIGGER workboard_label_limit BEFORE INSERT ON workboard_card_labels
	 WHEN (SELECT count(*) FROM workboard_card_labels WHERE board_id=NEW.board_id AND card_id=NEW.card_id)>=32
	 BEGIN SELECT RAISE(ABORT,'workboard label limit'); END;
	CREATE TRIGGER workboard_attempt_task_limit BEFORE INSERT ON workboard_attempt_tasks
	 WHEN (SELECT count(*) FROM workboard_attempt_tasks WHERE board_id=NEW.board_id AND card_id=NEW.card_id AND attempt_id=NEW.attempt_id)>=128
	 BEGIN SELECT RAISE(ABORT,'workboard attempt task limit'); END;
	CREATE TRIGGER workboard_attempt_session_limit BEFORE INSERT ON workboard_attempt_sessions
	 WHEN (SELECT count(*) FROM workboard_attempt_sessions WHERE board_id=NEW.board_id AND card_id=NEW.card_id AND attempt_id=NEW.attempt_id)>=128
	 BEGIN SELECT RAISE(ABORT,'workboard attempt session limit'); END;
	CREATE TRIGGER workboard_candidate_artifact_limit BEFORE INSERT ON workboard_candidate_artifacts
	 WHEN (SELECT count(*) FROM workboard_candidate_artifacts WHERE board_id=NEW.board_id AND card_id=NEW.card_id AND attempt_id=NEW.attempt_id AND candidate_id=NEW.candidate_id)>=32
	 BEGIN SELECT RAISE(ABORT,'workboard candidate artifact limit'); END;
	CREATE TRIGGER workboard_checkpoint_limit BEFORE INSERT ON workboard_checkpoints
	 WHEN (SELECT count(*) FROM workboard_checkpoints WHERE board_id=NEW.board_id AND card_id=NEW.card_id AND attempt_id=NEW.attempt_id)>=10000
	 BEGIN SELECT RAISE(ABORT,'workboard checkpoint limit'); END;`)
	if err != nil {
		return err
	}
	if err = validateWorkboardSchema36(ctx, conn); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "PRAGMA user_version=36")
	return err
}

func validateWorkboardSchema(ctx context.Context, conn *sql.Conn) error {
	return validateWorkboardSchemaVersion(ctx, conn, false, false)
}

func validateWorkboardSchema36(ctx context.Context, conn *sql.Conn) error {
	return validateWorkboardSchemaVersion(ctx, conn, true, false)
}

func validateWorkboardSchema40(ctx context.Context, conn *sql.Conn) error {
	return validateWorkboardSchemaVersion(ctx, conn, true, true)
}

func validateWorkboardSchema35WithReassignments(ctx context.Context, conn *sql.Conn) error {
	return validateWorkboardSchemaVersion(ctx, conn, false, true)
}

func validateWorkboardSchemaVersion(ctx context.Context, conn *sql.Conn, eventCardIdentity, reassignments bool) error {
	shapes := map[string]string{
		"workboard_boards":              "id:TEXT:0:1,revision:INTEGER:1:0,layout_revision:INTEGER:1:0,event_sequence:INTEGER:1:0,graph_revision:INTEGER:1:0,graph_digest:TEXT:1:0,state:TEXT:1:0,title:TEXT:1:0,description:TEXT:1:0,card_count:INTEGER:1:0,active_claims:INTEGER:1:0,created_at:INTEGER:1:0,updated_at:INTEGER:1:0,body:BLOB:1:0",
		"workboard_columns":             "board_id:TEXT:1:1,version:INTEGER:1:0,id:TEXT:1:0,state:TEXT:1:2,ordinal:INTEGER:1:0,rank:TEXT:1:0,title:TEXT:1:0",
		"workboard_cards":               "id:TEXT:0:1,board_id:TEXT:1:0,revision:INTEGER:1:0,criteria_revision:INTEGER:1:0,state:TEXT:1:0,rank:TEXT:1:0,title:TEXT:1:0,description:TEXT:1:0,priority:TEXT:1:0,parent_id:TEXT:0:0,assignee_id:TEXT:0:0,block_reason:TEXT:0:0,remaining_dependencies:INTEGER:1:0,attempt_count:INTEGER:1:0,attempt_limit:INTEGER:1:0,time_limit_ms:INTEGER:1:0,token_limit:INTEGER:1:0,cost_micros:INTEGER:1:0,current_attempt_id:TEXT:0:0,current_claim_id:TEXT:0:0,acceptance_id:TEXT:0:0,cancel_requested:INTEGER:1:0,pause_requested:INTEGER:1:0,created_at:INTEGER:1:0,updated_at:INTEGER:1:0,body:BLOB:1:0",
		"workboard_card_labels":         "board_id:TEXT:1:1,card_id:TEXT:1:2,ordinal:INTEGER:1:3,label:TEXT:1:0,label_key:TEXT:1:0",
		"workboard_dependencies":        "board_id:TEXT:1:1,card_id:TEXT:1:2,dependency_id:TEXT:1:3,created_sequence:INTEGER:1:0",
		"workboard_attempts":            "id:TEXT:0:1,board_id:TEXT:1:0,card_id:TEXT:1:0,ordinal:INTEGER:1:0,revision:INTEGER:1:0,state:TEXT:1:0,worker_id:TEXT:1:0,criteria_revision:INTEGER:1:0,criteria_digest:TEXT:1:0,policy_digest:TEXT:1:0,attempt_limit:INTEGER:1:0,time_limit_ms:INTEGER:1:0,token_limit:INTEGER:1:0,cost_micros:INTEGER:1:0,started_at:INTEGER:1:0,ended_at:INTEGER:0:0,body:BLOB:1:0",
		"workboard_attempt_tasks":       "board_id:TEXT:1:1,card_id:TEXT:1:2,attempt_id:TEXT:1:3,ordinal:INTEGER:1:4,task_id:TEXT:1:0",
		"workboard_attempt_sessions":    "board_id:TEXT:1:1,card_id:TEXT:1:2,attempt_id:TEXT:1:3,ordinal:INTEGER:1:4,session_id:TEXT:1:0",
		"workboard_criteria":            "board_id:TEXT:1:1,card_id:TEXT:1:2,criteria_revision:INTEGER:1:3,ordinal:INTEGER:1:0,id:TEXT:1:4,kind:TEXT:1:0,required_source:TEXT:1:0,validator_id:TEXT:1:0,description:TEXT:1:0,required:INTEGER:1:0,body:BLOB:1:0",
		"workboard_claims":              "id:TEXT:0:1,board_id:TEXT:1:0,card_id:TEXT:1:0,attempt_id:TEXT:1:0,revision:INTEGER:1:0,state:TEXT:1:0,owner_id:TEXT:1:0,owner_type:TEXT:1:0,task_id:TEXT:0:0,expires_at:INTEGER:1:0,last_heartbeat:INTEGER:1:0,released_at:INTEGER:0:0,body:BLOB:1:0",
		"workboard_claim_heartbeats":    "claim_id:TEXT:1:1,revision:INTEGER:1:2,observed_at:INTEGER:1:0,expires_at:INTEGER:1:0,actor_id:TEXT:1:0,body:BLOB:1:0",
		"workboard_checkpoints":         "id:TEXT:0:1,board_id:TEXT:1:0,card_id:TEXT:1:0,attempt_id:TEXT:1:0,claim_id:TEXT:1:0,revision:INTEGER:1:0,claim_revision:INTEGER:1:0,criteria_revision:INTEGER:1:0,criteria_digest:TEXT:1:0,policy_digest:TEXT:1:0,evidence:TEXT:1:0,evidence_digest:TEXT:1:0,actor_id:TEXT:1:0,actor_type:TEXT:1:0,created_at:INTEGER:1:0,body:BLOB:1:0",
		"workboard_candidates":          "id:TEXT:0:1,board_id:TEXT:1:0,card_id:TEXT:1:0,attempt_id:TEXT:1:0,revision:INTEGER:1:0,digest:TEXT:1:0,criteria_digest:TEXT:1:0,policy_digest:TEXT:1:0,evidence_digest:TEXT:1:0,evidence_count:INTEGER:1:0,submitted_by:TEXT:1:0,created_at:INTEGER:1:0,body:BLOB:1:0",
		"workboard_candidate_artifacts": "board_id:TEXT:1:1,card_id:TEXT:1:2,attempt_id:TEXT:1:3,candidate_id:TEXT:1:4,ordinal:INTEGER:1:5,artifact_ref:TEXT:1:0",
		"workboard_evidence":            "id:TEXT:0:1,board_id:TEXT:1:0,card_id:TEXT:1:0,attempt_id:TEXT:1:0,candidate_id:TEXT:1:0,criterion_id:TEXT:1:0,revision:INTEGER:1:0,source:TEXT:1:0,outcome:TEXT:1:0,actor_id:TEXT:1:0,actor_type:TEXT:1:0,reference:TEXT:1:0,candidate_digest:TEXT:1:0,criteria_revision:INTEGER:1:0,criteria_digest:TEXT:1:0,policy_digest:TEXT:1:0,created_at:INTEGER:1:0,body:BLOB:1:0",
		"workboard_acceptances":         "id:TEXT:0:1,board_id:TEXT:1:0,card_id:TEXT:1:0,attempt_id:TEXT:1:0,candidate_id:TEXT:1:0,candidate_digest:TEXT:1:0,criteria_revision:INTEGER:1:0,criteria_digest:TEXT:1:0,evidence_head_revision:INTEGER:1:0,evidence_set_digest:TEXT:1:0,policy_digest:TEXT:1:0,decision:TEXT:1:0,decided_by:TEXT:1:0,decided_by_type:TEXT:1:0,decision_authority_id:TEXT:1:0,decided_at:INTEGER:1:0,body:BLOB:1:0",
		"workboard_recovery_proofs":     "id:TEXT:0:1,board_id:TEXT:1:0,card_id:TEXT:1:0,attempt_id:TEXT:1:0,claim_id:TEXT:1:0,task_head_digest:TEXT:1:0,process_proof_digest:TEXT:1:0,effect_evidence_digest:TEXT:1:0,effect_resolution:TEXT:1:0,created_at:INTEGER:1:0,body:BLOB:1:0",
		"workboard_recoveries":          "id:TEXT:0:1,proof_id:TEXT:1:0,board_id:TEXT:1:0,card_id:TEXT:1:0,attempt_id:TEXT:1:0,old_claim_id:TEXT:1:0,old_claim_revision:INTEGER:1:0,card_revision:INTEGER:1:0,first_sequence:INTEGER:1:0,last_sequence:INTEGER:1:0,recovered_at:INTEGER:1:0,body:BLOB:1:0",
		"workboard_operations":          "scope_kind:TEXT:1:1,scope_id:TEXT:1:2,key_digest:TEXT:1:3,operation_id:TEXT:1:0,board_id:TEXT:1:0,request_digest:TEXT:1:0,response_digest:TEXT:1:0,first_sequence:INTEGER:1:0,last_sequence:INTEGER:1:0,event_count:INTEGER:1:0,transaction_bytes:INTEGER:1:0,outcome:TEXT:1:0,response:BLOB:1:0,created_at:INTEGER:1:0",
		"workboard_events":              "id:TEXT:1:0,board_id:TEXT:1:1,sequence:INTEGER:1:2,operation_id:TEXT:1:0,kind:TEXT:1:0,actor_id:TEXT:1:0,actor_type:TEXT:1:0,created_at:INTEGER:1:0,body:BLOB:1:0",
	}
	if eventCardIdentity {
		shapes["workboard_events"] = "id:TEXT:1:0,board_id:TEXT:1:1,sequence:INTEGER:1:2,operation_id:TEXT:1:0,kind:TEXT:1:0,actor_id:TEXT:1:0,actor_type:TEXT:1:0,card_id:TEXT:0:0,created_at:INTEGER:1:0,body:BLOB:1:0"
		if browserTableShape(ctx, conn, "workboard_events", "id:TEXT:1:0,board_id:TEXT:1:1,sequence:INTEGER:1:2,operation_id:TEXT:1:0,kind:TEXT:1:0,actor_id:TEXT:1:0,actor_type:TEXT:1:0,card_id:TEXT:0:0,created_at:INTEGER:1:0,body:BLOB:1:0,decomposition_admission_id:TEXT:0:0,decomposition_admission_digest:TEXT:0:0,decomposition_decision_digest:TEXT:0:0,decomposition_config_digest:TEXT:0:0,decomposition_policy_digest:TEXT:0:0,decomposition_max_depth:INTEGER:0:0,decomposition_max_children:INTEGER:0:0,decomposition_depth:INTEGER:0:0,decomposition_direct_children:INTEGER:0:0") {
			shapes["workboard_events"] = "id:TEXT:1:0,board_id:TEXT:1:1,sequence:INTEGER:1:2,operation_id:TEXT:1:0,kind:TEXT:1:0,actor_id:TEXT:1:0,actor_type:TEXT:1:0,card_id:TEXT:0:0,created_at:INTEGER:1:0,body:BLOB:1:0,decomposition_admission_id:TEXT:0:0,decomposition_admission_digest:TEXT:0:0,decomposition_decision_digest:TEXT:0:0,decomposition_config_digest:TEXT:0:0,decomposition_policy_digest:TEXT:0:0,decomposition_max_depth:INTEGER:0:0,decomposition_max_children:INTEGER:0:0,decomposition_depth:INTEGER:0:0,decomposition_direct_children:INTEGER:0:0"
		}
	}
	if reassignments {
		shapes["workboard_reassignments"] = "recovery_id:TEXT:1:1,board_id:TEXT:1:0,card_id:TEXT:1:0,predecessor_attempt_id:TEXT:1:0,predecessor_claim_id:TEXT:1:0,successor_attempt_id:TEXT:1:0,successor_claim_id:TEXT:1:0,created_at:INTEGER:1:0,body:BLOB:1:0"
	}
	for table, shape := range shapes {
		if !browserTableShape(ctx, conn, table, shape) {
			return sql.ErrNoRows
		}
	}
	tables := []string{
		"workboard_acceptances", "workboard_attempt_sessions", "workboard_attempt_tasks", "workboard_attempts",
		"workboard_boards", "workboard_candidate_artifacts", "workboard_candidates", "workboard_card_labels",
		"workboard_cards", "workboard_checkpoints", "workboard_claim_heartbeats", "workboard_claims", "workboard_columns",
		"workboard_criteria", "workboard_dependencies", "workboard_events", "workboard_evidence",
		"workboard_operations", "workboard_recoveries", "workboard_recovery_proofs",
	}
	if reassignments {
		tables = append(tables[:18], append([]string{"workboard_reassignments"}, tables[18:]...)...)
	}
	if err := workboardNamedObjects(ctx, conn, "table", tables); err != nil {
		return err
	}
	rules := map[string][]string{
		"workboard_boards":          {"check(state!='archived'oractive_claims=0)", "check(length(body)between1and1048576)"},
		"workboard_columns":         {"check(id=state)", "unique(board_id,ordinal)", "referencesworkboard_boards(id)ondeletecascade"},
		"workboard_cards":           {"unique(board_id,state,rank)", "check(state!='ready'orremaining_dependencies=0)", "check((state='blocked'andblock_reasonisnotnull)or(state!='blocked'andblock_reasonisnull))", "foreignkey(board_id,parent_id)referencesworkboard_cards(board_id,id)", "foreignkey(board_id,id,current_attempt_id)referencesworkboard_attempts(board_id,card_id,id)deferrableinitiallydeferred"},
		"workboard_dependencies":    {"check(card_id!=dependency_id)", "foreignkey(board_id,dependency_id)referencesworkboard_cards(board_id,id)"},
		"workboard_attempts":        {"unique(board_id,card_id,ordinal)", "check((state='running'andended_atisnull)or(state!='running'andended_atisnotnull))"},
		"workboard_criteria":        {"unique(board_id,card_id,criteria_revision,ordinal)", "check((kind='objective'andrequired_source='deterministic')or(kind='subjective'andrequired_source='user_feedback'))"},
		"workboard_claims":          {"task_idtextreferencestask_heads(task_id)", "check(task_idisnullorlength(cast(task_idasblob))between1and128)", "unique(board_id,card_id,attempt_id,id)", "check(last_heartbeat>=0andexpires_at-last_heartbeatbetween1and600000000000)"},
		"workboard_checkpoints":     {"check(revisionbetween1and10000)", "foreignkey(board_id,card_id,attempt_id,claim_id)referencesworkboard_claims(board_id,card_id,attempt_id,id)"},
		"workboard_evidence":        {"referencesworkboard_candidates(board_id,card_id,attempt_id,id)ondeletecascade", "referencesworkboard_criteria(board_id,card_id,criteria_revision,id)"},
		"workboard_recovery_proofs": {"foreignkey(board_id,card_id,attempt_id,claim_id)referencesworkboard_claims(board_id,card_id,attempt_id,id)"},
		"workboard_recoveries":      {"foreignkey(board_id,card_id,attempt_id,old_claim_id,proof_id)referencesworkboard_recovery_proofs(board_id,card_id,attempt_id,claim_id,id)"},
		"workboard_operations":      {"primarykey(scope_kind,scope_id,key_digest)", "operation_idtextnotnullunique", "unique(board_id,operation_id)", "check(outcome='committed')", "check(scope_kind!='board'orscope_id=board_id)"},
		"workboard_events":          {"foreignkey(board_id,operation_id)referencesworkboard_operations(board_id,operation_id)deferrableinitiallydeferred", "primarykey(board_id,sequence)"},
	}
	if eventCardIdentity {
		rules["workboard_events"] = append(rules["workboard_events"], "foreignkey(board_id,card_id)referencesworkboard_cards(board_id,id)")
	}
	if reassignments {
		rules["workboard_reassignments"] = []string{
			"primarykey(recovery_id)",
			"unique(board_id,card_id,successor_attempt_id)",
			"unique(board_id,card_id,successor_claim_id)",
			"foreignkey(board_id,card_id,predecessor_attempt_id,predecessor_claim_id,recovery_id)referencesworkboard_recoveries(board_id,card_id,attempt_id,old_claim_id,id)",
			"foreignkey(board_id,card_id,predecessor_attempt_id,predecessor_claim_id)referencesworkboard_claims(board_id,card_id,attempt_id,id)",
			"foreignkey(board_id,card_id,successor_attempt_id,successor_claim_id)referencesworkboard_claims(board_id,card_id,attempt_id,id)",
			"check(predecessor_attempt_id!=successor_attempt_id)",
			"check(predecessor_claim_id!=successor_claim_id)",
		}
	}
	for table, expected := range rules {
		if !browserTableRules(ctx, conn, table, expected) {
			return sql.ErrNoRows
		}
	}
	indexes := []string{
		"workboard_attempts_active", "workboard_cards_board_state_rank", "workboard_cards_parent",
		"workboard_checkpoints_attempt", "workboard_claims_active", "workboard_claims_expiry", "workboard_dependencies_reverse",
		"workboard_events_operation", "workboard_evidence_attempt", "workboard_operations_board_created",
	}
	if reassignments {
		indexes = append(indexes, "workboard_reassignments_card", "workboard_recoveries_identity")
	}
	if err := workboardNamedObjects(ctx, conn, "index", indexes); err != nil {
		return err
	}
	objects := map[string][]string{
		"workboard_attempts_active":          {"onworkboard_attempts(board_id,card_id)wherestatein('running','review')"},
		"workboard_cards_board_state_rank":   {"onworkboard_cards(board_id,state,rank,id)"},
		"workboard_cards_parent":             {"onworkboard_cards(board_id,parent_id,id)"},
		"workboard_claims_active":            {"onworkboard_claims(board_id,card_id)wherestatein('active','attention')"},
		"workboard_claims_expiry":            {"onworkboard_claims(state,expires_at,id)"},
		"workboard_checkpoints_attempt":      {"onworkboard_checkpoints(board_id,card_id,attempt_id,revision)"},
		"workboard_dependencies_reverse":     {"onworkboard_dependencies(board_id,dependency_id,card_id)"},
		"workboard_events_operation":         {"onworkboard_events(operation_id,sequence)"},
		"workboard_evidence_attempt":         {"onworkboard_evidence(board_id,card_id,attempt_id,revision)"},
		"workboard_operations_board_created": {"onworkboard_operations(board_id,created_at,operation_id)"},
		"workboard_board_limit":              {"beforeinsertonworkboard_boards", "count(*)fromworkboard_boards)>=100"},
		"workboard_card_limit":               {"beforeinsertonworkboard_cards", "count(*)fromworkboard_cardswhereboard_id=new.board_id)>=10000"},
		"workboard_dependency_limit":         {"beforeinsertonworkboard_dependencies", "card_id=new.card_id)>=64"},
		"workboard_reverse_fanout_limit":     {"beforeinsertonworkboard_dependencies", "dependency_id=new.dependency_id)>=64"},
		"workboard_criteria_limit":           {"beforeinsertonworkboard_criteria", "criteria_revision=new.criteria_revision)>=32"},
		"workboard_evidence_limit":           {"beforeinsertonworkboard_evidence", "attempt_id=new.attempt_id)>=100"},
		"workboard_label_limit":              {"beforeinsertonworkboard_card_labels", "card_id=new.card_id)>=32"},
		"workboard_attempt_task_limit":       {"beforeinsertonworkboard_attempt_tasks", "attempt_id=new.attempt_id)>=128"},
		"workboard_attempt_session_limit":    {"beforeinsertonworkboard_attempt_sessions", "attempt_id=new.attempt_id)>=128"},
		"workboard_candidate_artifact_limit": {"beforeinsertonworkboard_candidate_artifacts", "candidate_id=new.candidate_id)>=32"},
		"workboard_checkpoint_limit":         {"beforeinsertonworkboard_checkpoints", "attempt_id=new.attempt_id)>=10000"},
	}
	if reassignments {
		objects["workboard_reassignments_card"] = []string{"onworkboard_reassignments(board_id,card_id,created_at,recovery_id)"}
		objects["workboard_recoveries_identity"] = []string{"uniqueindexworkboard_recoveries_identityonworkboard_recoveries(board_id,card_id,attempt_id,old_claim_id,id)"}
		objects["workboard_reassignment_immutable_delete"] = []string{"beforedeleteonworkboard_reassignments", "raise(abort,'workboardreassignmentisimmutable')"}
		objects["workboard_reassignment_immutable_update"] = []string{"beforeupdateonworkboard_reassignments", "raise(abort,'workboardreassignmentisimmutable')"}
	}
	for name, expected := range objects {
		kind := "index"
		if len(name) > len("workboard_") && (name == "workboard_board_limit" || name == "workboard_card_limit" ||
			name == "workboard_dependency_limit" || name == "workboard_reverse_fanout_limit" || name == "workboard_criteria_limit" ||
			name == "workboard_evidence_limit" || name == "workboard_label_limit" || name == "workboard_attempt_task_limit" ||
			name == "workboard_attempt_session_limit" || name == "workboard_candidate_artifact_limit" || name == "workboard_checkpoint_limit" ||
			name == "workboard_reassignment_immutable_delete" || name == "workboard_reassignment_immutable_update") {
			kind = "trigger"
		}
		if !workboardObjectRules(ctx, conn, kind, name, expected) {
			return sql.ErrNoRows
		}
	}
	rows, err := conn.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() || rows.Err() != nil {
		if err = rows.Err(); err != nil {
			return err
		}
		return sql.ErrNoRows
	}
	return nil
}

func workboardObjectRules(ctx context.Context, conn *sql.Conn, kind, name string, expected []string) bool {
	var definition string
	if err := conn.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type=? AND name=?", kind, name).Scan(&definition); err != nil {
		return false
	}
	normalized := strings.ToLower(strings.Join(strings.Fields(definition), ""))
	for _, rule := range expected {
		if !strings.Contains(normalized, strings.ReplaceAll(rule, " ", "")) {
			return false
		}
	}
	return true
}

func workboardNamedObjects(ctx context.Context, conn *sql.Conn, kind string, expected []string) error {
	// Schema-49 decomposition admissions extend the Workboard namespace while
	// retaining the exact schema-40 core. Their own validator proves these
	// objects. Excluding only this closed set keeps the historical validators
	// useful during migration without making arbitrary future objects invisible.
	rows, err := conn.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type=? AND name GLOB 'workboard_*'
		AND name NOT IN ('workboard_decomposition_admissions','workboard_decomposition_admissions_parent','workboard_decomposition_admissions_card',
			'workboard_decomposition_admission_binding','workboard_decomposition_admission_immutable_update',
			'workboard_decomposition_admission_immutable_delete','workboard_decomposition_event_binding',
			'workboard_decomposition_event_immutable')
		ORDER BY name`, kind)
	if err != nil {
		return err
	}
	defer rows.Close()
	actual := make([]string, 0, len(expected))
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			return err
		}
		actual = append(actual, name)
	}
	if err = rows.Err(); err != nil || len(actual) != len(expected) {
		if err != nil {
			return err
		}
		return sql.ErrNoRows
	}
	for i := range expected {
		if actual[i] != expected[i] {
			return sql.ErrNoRows
		}
	}
	if kind == "index" {
		triggers := []string{
			"workboard_attempt_session_limit", "workboard_attempt_task_limit", "workboard_board_limit",
			"workboard_candidate_artifact_limit", "workboard_card_limit", "workboard_checkpoint_limit",
			"workboard_criteria_limit", "workboard_dependency_limit", "workboard_evidence_limit", "workboard_label_limit", "workboard_reverse_fanout_limit",
		}
		for _, name := range expected {
			if name == "workboard_reassignments_card" {
				triggers = append(triggers[:10], append([]string{
					"workboard_reassignment_immutable_delete", "workboard_reassignment_immutable_update",
				}, triggers[10:]...)...)
				break
			}
		}
		return workboardNamedObjects(ctx, conn, "trigger", triggers)
	}
	return nil
}
