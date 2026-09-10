package telemetry

import (
	"context"
	"database/sql"
	"errors"
)

// Schema 42 adds append-only execution admissions and settlements. It does not
// infer admissions from older task/claim pairs: doing so would invent WIP and
// resource reservations that were never atomically authorized.
func migrateWorkboardExecutionBudgets(ctx context.Context, conn *sql.Conn) error {
	var retained int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN
		('workboard_execution_admissions','workboard_execution_settlements')`).Scan(&retained); err != nil {
		return err
	}
	if retained != 0 {
		return errors.New("workboard execution budget tables exist before schema 42")
	}
	if err := validateTaskStartClaimSchema(ctx, conn); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `CREATE TABLE workboard_execution_admissions(
		admission_id TEXT PRIMARY KEY CHECK(length(CAST(admission_id AS BLOB)) BETWEEN 1 AND 128),
		task_id TEXT NOT NULL UNIQUE REFERENCES task_heads(task_id),
		session_id TEXT NOT NULL CHECK(length(CAST(session_id AS BLOB)) BETWEEN 1 AND 128),
		event_id TEXT NOT NULL UNIQUE REFERENCES events(id),
		event_digest TEXT NOT NULL CHECK(length(event_digest)=64 AND event_digest NOT GLOB '*[^0-9a-f]*'),
		board_id TEXT NOT NULL,
		card_id TEXT NOT NULL,
		attempt_id TEXT NOT NULL,
		claim_id TEXT NOT NULL,
		worker_id TEXT NOT NULL CHECK(length(CAST(worker_id AS BLOB)) BETWEEN 1 AND 128),
		operation_id TEXT NOT NULL UNIQUE,
		request_digest TEXT NOT NULL CHECK(length(request_digest)=64 AND request_digest NOT GLOB '*[^0-9a-f]*'),
		card_revision INTEGER NOT NULL CHECK(card_revision>0),
		model_id TEXT NOT NULL CHECK(length(CAST(model_id AS BLOB)) BETWEEN 1 AND 512),
		provider_id TEXT NOT NULL CHECK(length(CAST(provider_id AS BLOB)) BETWEEN 1 AND 128),
		config_id TEXT NOT NULL CHECK(length(config_id)=64 AND config_id NOT GLOB '*[^0-9a-f]*'),
		policy_digest TEXT NOT NULL CHECK(length(policy_digest)=64 AND policy_digest NOT GLOB '*[^0-9a-f]*'),
		time_limit_ms INTEGER NOT NULL CHECK(time_limit_ms BETWEEN 0 AND 2592000000),
		token_limit INTEGER NOT NULL CHECK(token_limit BETWEEN 0 AND 1000000000),
		cost_micros INTEGER NOT NULL CHECK(cost_micros BETWEEN 0 AND 1000000000000),
		global_wip_limit INTEGER NOT NULL CHECK(global_wip_limit BETWEEN 1 AND 1024),
		board_wip_limit INTEGER NOT NULL CHECK(board_wip_limit BETWEEN 1 AND 1024 AND board_wip_limit<=global_wip_limit),
		admitted_at INTEGER NOT NULL CHECK(admitted_at>=0),
		admission_digest TEXT NOT NULL UNIQUE CHECK(length(admission_digest)=64 AND admission_digest NOT GLOB '*[^0-9a-f]*'),
		body BLOB NOT NULL CHECK(length(body) BETWEEN 1 AND 32768),
		FOREIGN KEY(task_id) REFERENCES workboard_task_start_claims(task_id),
		FOREIGN KEY(board_id,operation_id) REFERENCES workboard_operations(board_id,operation_id),
		FOREIGN KEY(board_id,card_id,attempt_id,claim_id) REFERENCES workboard_claims(board_id,card_id,attempt_id,id));
	CREATE TABLE workboard_execution_settlements(
		settlement_id TEXT PRIMARY KEY CHECK(length(CAST(settlement_id AS BLOB)) BETWEEN 1 AND 128),
		admission_id TEXT NOT NULL UNIQUE REFERENCES workboard_execution_admissions(admission_id),
		admission_digest TEXT NOT NULL CHECK(length(admission_digest)=64 AND admission_digest NOT GLOB '*[^0-9a-f]*'),
		task_id TEXT NOT NULL UNIQUE REFERENCES task_heads(task_id),
		session_id TEXT NOT NULL CHECK(length(CAST(session_id AS BLOB)) BETWEEN 1 AND 128),
		board_id TEXT NOT NULL,
		card_id TEXT NOT NULL,
		attempt_id TEXT NOT NULL,
		claim_id TEXT NOT NULL,
		worker_id TEXT NOT NULL CHECK(length(CAST(worker_id AS BLOB)) BETWEEN 1 AND 128),
		operation_id TEXT NOT NULL UNIQUE,
		model_id TEXT NOT NULL CHECK(length(CAST(model_id AS BLOB)) BETWEEN 1 AND 512),
		provider_id TEXT NOT NULL CHECK(length(CAST(provider_id AS BLOB)) BETWEEN 1 AND 128),
		config_id TEXT NOT NULL CHECK(length(config_id)=64 AND config_id NOT GLOB '*[^0-9a-f]*'),
		terminal_event_id TEXT NOT NULL UNIQUE REFERENCES events(id),
		terminal_event_digest TEXT NOT NULL CHECK(length(terminal_event_digest)=64 AND terminal_event_digest NOT GLOB '*[^0-9a-f]*'),
		terminal_kind TEXT NOT NULL CHECK(terminal_kind IN('task.completed','task.failed','task.canceled')),
		terminal_sequence INTEGER NOT NULL CHECK(terminal_sequence>1),
		charged_time_ms INTEGER NOT NULL CHECK(charged_time_ms BETWEEN 0 AND 2592000000),
		charged_tokens INTEGER NOT NULL CHECK(charged_tokens BETWEEN 0 AND 1000000000),
		charged_cost_micros INTEGER NOT NULL CHECK(charged_cost_micros BETWEEN 0 AND 1000000000000),
		token_usage_known INTEGER NOT NULL CHECK(token_usage_known IN(0,1)),
		settled_at INTEGER NOT NULL CHECK(settled_at>=0),
		settlement_digest TEXT NOT NULL UNIQUE CHECK(length(settlement_digest)=64 AND settlement_digest NOT GLOB '*[^0-9a-f]*'),
		body BLOB NOT NULL CHECK(length(body) BETWEEN 1 AND 32768));
	CREATE INDEX workboard_execution_admissions_global ON workboard_execution_admissions(admitted_at,task_id);
	CREATE INDEX workboard_execution_admissions_board ON workboard_execution_admissions(board_id,admitted_at,task_id);
	CREATE INDEX workboard_execution_admissions_card ON workboard_execution_admissions(board_id,card_id,admitted_at,task_id);
	CREATE INDEX workboard_execution_settlements_board ON workboard_execution_settlements(board_id,card_id,settled_at,task_id);
	CREATE TRIGGER workboard_execution_admission_no_active BEFORE INSERT ON workboard_execution_admissions
		WHEN EXISTS(SELECT 1 FROM workboard_execution_admissions a
			WHERE a.board_id=NEW.board_id AND a.card_id=NEW.card_id
			AND NOT EXISTS(SELECT 1 FROM workboard_execution_settlements s WHERE s.admission_id=a.admission_id))
		BEGIN SELECT RAISE(ABORT,'workboard card already has an active execution admission'); END;
	CREATE TRIGGER workboard_execution_admission_binding BEFORE INSERT ON workboard_execution_admissions
		WHEN NOT EXISTS(SELECT 1 FROM workboard_task_start_claims c
			WHERE c.task_id=NEW.task_id AND c.session_id=NEW.session_id AND c.event_id=NEW.event_id AND c.event_digest=NEW.event_digest
			AND c.board_id=NEW.board_id AND c.card_id=NEW.card_id AND c.attempt_id=NEW.attempt_id AND c.claim_id=NEW.claim_id
			AND c.worker_id=NEW.worker_id AND c.operation_id=NEW.operation_id AND c.request_digest=NEW.request_digest
			AND c.expected_card_revision=NEW.card_revision AND c.policy_digest=NEW.policy_digest)
		OR NOT EXISTS(SELECT 1 FROM events e JOIN event_log l ON l.event_id=e.id
			WHERE e.id=NEW.event_id AND e.task_id=NEW.task_id AND e.sequence=1 AND l.task_id=NEW.task_id AND l.task_sequence=1
			AND l.body_digest=NEW.event_digest
			AND json_extract(e.body,'$.session_id')=NEW.session_id AND json_extract(e.body,'$.worker_id')=NEW.worker_id
			AND json_extract(e.body,'$.kind')='task.started' AND json_extract(e.body,'$.data.model_id')=NEW.model_id
			AND json_extract(e.body,'$.data.provider_id')=NEW.provider_id AND json_extract(e.body,'$.data.config_id')=NEW.config_id)
		BEGIN SELECT RAISE(ABORT,'workboard execution admission binding mismatch'); END;
	CREATE TRIGGER workboard_execution_settlement_binding BEFORE INSERT ON workboard_execution_settlements
		WHEN NOT EXISTS(SELECT 1 FROM workboard_execution_admissions a
			WHERE a.admission_id=NEW.admission_id AND a.admission_digest=NEW.admission_digest AND a.task_id=NEW.task_id
			AND a.session_id=NEW.session_id AND a.board_id=NEW.board_id AND a.card_id=NEW.card_id AND a.attempt_id=NEW.attempt_id
			AND a.claim_id=NEW.claim_id AND a.worker_id=NEW.worker_id AND a.operation_id=NEW.operation_id
			AND a.model_id=NEW.model_id AND a.provider_id=NEW.provider_id AND a.config_id=NEW.config_id)
		OR NOT EXISTS(SELECT 1 FROM events e JOIN event_log l ON l.event_id=e.id
			WHERE e.id=NEW.terminal_event_id AND e.task_id=NEW.task_id AND e.sequence=NEW.terminal_sequence
			AND l.task_id=NEW.task_id AND l.task_sequence=NEW.terminal_sequence AND l.body_digest=NEW.terminal_event_digest
			AND json_extract(e.body,'$.session_id')=NEW.session_id
			AND json_extract(e.body,'$.kind')=NEW.terminal_kind)
		OR NOT EXISTS(SELECT 1 FROM workboard_attempts a JOIN workboard_claims c
			ON c.board_id=a.board_id AND c.card_id=a.card_id AND c.attempt_id=a.id
			WHERE a.board_id=NEW.board_id AND a.card_id=NEW.card_id AND a.id=NEW.attempt_id AND a.state!='running' AND a.ended_at IS NOT NULL
			AND c.id=NEW.claim_id AND c.state='released' AND c.released_at IS NOT NULL)
		BEGIN SELECT RAISE(ABORT,'workboard execution settlement binding mismatch'); END;
	CREATE TRIGGER workboard_execution_admission_immutable_update BEFORE UPDATE ON workboard_execution_admissions
		BEGIN SELECT RAISE(ABORT,'workboard execution admission is immutable'); END;
	CREATE TRIGGER workboard_execution_admission_immutable_delete BEFORE DELETE ON workboard_execution_admissions
		BEGIN SELECT RAISE(ABORT,'workboard execution admission is immutable'); END;
	CREATE TRIGGER workboard_execution_settlement_immutable_update BEFORE UPDATE ON workboard_execution_settlements
		BEGIN SELECT RAISE(ABORT,'workboard execution settlement is immutable'); END;
	CREATE TRIGGER workboard_execution_settlement_immutable_delete BEFORE DELETE ON workboard_execution_settlements
		BEGIN SELECT RAISE(ABORT,'workboard execution settlement is immutable'); END;
	PRAGMA user_version=42;`); err != nil {
		return err
	}
	return validateWorkboardExecutionBudgetSchema(ctx, conn)
}

func validateWorkboardExecutionBudgetSchema(ctx context.Context, conn *sql.Conn) error {
	if err := validateTaskStartClaimSchema(ctx, conn); err != nil {
		return err
	}
	if !browserTableShape(ctx, conn, "workboard_execution_admissions",
		"admission_id:TEXT:0:1,task_id:TEXT:1:0,session_id:TEXT:1:0,event_id:TEXT:1:0,event_digest:TEXT:1:0,board_id:TEXT:1:0,card_id:TEXT:1:0,attempt_id:TEXT:1:0,claim_id:TEXT:1:0,worker_id:TEXT:1:0,operation_id:TEXT:1:0,request_digest:TEXT:1:0,card_revision:INTEGER:1:0,model_id:TEXT:1:0,provider_id:TEXT:1:0,config_id:TEXT:1:0,policy_digest:TEXT:1:0,time_limit_ms:INTEGER:1:0,token_limit:INTEGER:1:0,cost_micros:INTEGER:1:0,global_wip_limit:INTEGER:1:0,board_wip_limit:INTEGER:1:0,admitted_at:INTEGER:1:0,admission_digest:TEXT:1:0,body:BLOB:1:0") ||
		!browserTableShape(ctx, conn, "workboard_execution_settlements",
			"settlement_id:TEXT:0:1,admission_id:TEXT:1:0,admission_digest:TEXT:1:0,task_id:TEXT:1:0,session_id:TEXT:1:0,board_id:TEXT:1:0,card_id:TEXT:1:0,attempt_id:TEXT:1:0,claim_id:TEXT:1:0,worker_id:TEXT:1:0,operation_id:TEXT:1:0,model_id:TEXT:1:0,provider_id:TEXT:1:0,config_id:TEXT:1:0,terminal_event_id:TEXT:1:0,terminal_event_digest:TEXT:1:0,terminal_kind:TEXT:1:0,terminal_sequence:INTEGER:1:0,charged_time_ms:INTEGER:1:0,charged_tokens:INTEGER:1:0,charged_cost_micros:INTEGER:1:0,token_usage_known:INTEGER:1:0,settled_at:INTEGER:1:0,settlement_digest:TEXT:1:0,body:BLOB:1:0") {
		return errors.New("invalid workboard execution budget table shape")
	}
	for name, rules := range map[string][]string{
		"workboard_execution_admissions":  {"task_idtextnotnulluniquereferences", "foreignkey(task_id)referencesworkboard_task_start_claims(task_id)", "check(board_wip_limitbetween1and1024andboard_wip_limit<=global_wip_limit)"},
		"workboard_execution_settlements": {"admission_idtextnotnulluniquereferences", "terminal_kindtextnotnullcheck(terminal_kindin('task.completed','task.failed','task.canceled'))", "check(token_usage_knownin(0,1))"},
	} {
		if !browserTableRules(ctx, conn, name, rules) {
			return errors.New("invalid " + name + " table rules")
		}
	}
	for name, rule := range map[string]string{
		"workboard_execution_admissions_global": "onworkboard_execution_admissions(admitted_at,task_id)",
		"workboard_execution_admissions_board":  "onworkboard_execution_admissions(board_id,admitted_at,task_id)",
		"workboard_execution_admissions_card":   "onworkboard_execution_admissions(board_id,card_id,admitted_at,task_id)",
		"workboard_execution_settlements_board": "onworkboard_execution_settlements(board_id,card_id,settled_at,task_id)",
	} {
		if !workboardObjectRules(ctx, conn, "index", name, []string{rule}) {
			return errors.New("invalid " + name)
		}
	}
	for name, rules := range map[string][]string{
		"workboard_execution_admission_no_active":         {"beforeinsertonworkboard_execution_admissions", "alreadyhasanactiveexecutionadmission"},
		"workboard_execution_admission_binding":           {"beforeinsertonworkboard_execution_admissions", "l.body_digest=new.event_digest", "json_extract(e.body,'$.data.config_id')=new.config_id", "executionadmissionbindingmismatch"},
		"workboard_execution_settlement_binding":          {"beforeinsertonworkboard_execution_settlements", "l.body_digest=new.terminal_event_digest", "executionsettlementbindingmismatch"},
		"workboard_execution_admission_immutable_update":  {"beforeupdateonworkboard_execution_admissions", "executionadmissionisimmutable"},
		"workboard_execution_admission_immutable_delete":  {"beforedeleteonworkboard_execution_admissions", "executionadmissionisimmutable"},
		"workboard_execution_settlement_immutable_update": {"beforeupdateonworkboard_execution_settlements", "executionsettlementisimmutable"},
		"workboard_execution_settlement_immutable_delete": {"beforedeleteonworkboard_execution_settlements", "executionsettlementisimmutable"},
	} {
		if !workboardObjectRules(ctx, conn, "trigger", name, rules) {
			return errors.New("invalid " + name)
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
