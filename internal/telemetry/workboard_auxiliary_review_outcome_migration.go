package telemetry

import (
	"context"
	"database/sql"
	"errors"
)

// Schema 45 adds the immutable bridge between a successful auxiliary review
// and the exact candidate, runtime result, audit, evidence set, and settlement
// it produced. Existing admissions are migration-sealed as legacy rather than
// being granted outcome authority that did not exist when they were admitted.
func migrateWorkboardAuxiliaryReviewOutcomes(ctx context.Context, conn *sql.Conn) error {
	if err := validateWorkboardAuxiliaryReviewSuccessorFenceSchema(ctx, conn); err != nil {
		return err
	}
	var retained int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN
		('workboard_auxiliary_review_outcomes','workboard_auxiliary_review_legacy_outcome_admissions')`).Scan(&retained); err != nil {
		return err
	}
	if retained != 0 {
		return errors.New("workboard auxiliary review outcome tables exist before schema 45")
	}
	if _, err := conn.ExecContext(ctx, `CREATE TABLE workboard_auxiliary_review_legacy_outcome_admissions(
		admission_id TEXT PRIMARY KEY REFERENCES workboard_auxiliary_review_admissions(admission_id),
		admission_digest TEXT NOT NULL UNIQUE CHECK(length(admission_digest)=64 AND admission_digest NOT GLOB '*[^0-9a-f]*'));
	INSERT INTO workboard_auxiliary_review_legacy_outcome_admissions(admission_id,admission_digest)
		SELECT admission_id,admission_digest FROM workboard_auxiliary_review_admissions;
	CREATE TRIGGER workboard_auxiliary_review_legacy_outcome_admission_sealed_insert
		BEFORE INSERT ON workboard_auxiliary_review_legacy_outcome_admissions
		BEGIN SELECT RAISE(ABORT,'workboard auxiliary review legacy outcome admissions are migration sealed'); END;
	CREATE TRIGGER workboard_auxiliary_review_legacy_outcome_admission_immutable_update
		BEFORE UPDATE ON workboard_auxiliary_review_legacy_outcome_admissions
		BEGIN SELECT RAISE(ABORT,'workboard auxiliary review legacy outcome admission is immutable'); END;
	CREATE TRIGGER workboard_auxiliary_review_legacy_outcome_admission_immutable_delete
		BEFORE DELETE ON workboard_auxiliary_review_legacy_outcome_admissions
		BEGIN SELECT RAISE(ABORT,'workboard auxiliary review legacy outcome admission is immutable'); END;

	CREATE TABLE workboard_auxiliary_review_outcomes(
		outcome_id TEXT PRIMARY KEY CHECK(length(CAST(outcome_id AS BLOB)) BETWEEN 1 AND 128),
		version INTEGER NOT NULL CHECK(version=1),
		admission_id TEXT NOT NULL UNIQUE REFERENCES workboard_auxiliary_review_admissions(admission_id),
		admission_digest TEXT NOT NULL CHECK(length(admission_digest)=64 AND admission_digest NOT GLOB '*[^0-9a-f]*'),
		operation_id TEXT NOT NULL UNIQUE CHECK(length(CAST(operation_id AS BLOB)) BETWEEN 1 AND 128),
		board_id TEXT NOT NULL,
		card_id TEXT NOT NULL,
		attempt_id TEXT NOT NULL,
		claim_id TEXT NOT NULL,
		candidate_id TEXT NOT NULL CHECK(length(CAST(candidate_id AS BLOB)) BETWEEN 1 AND 128),
		candidate_digest TEXT NOT NULL CHECK(length(candidate_digest)=64 AND candidate_digest NOT GLOB '*[^0-9a-f]*'),
		criteria_digest TEXT NOT NULL CHECK(length(criteria_digest)=64 AND criteria_digest NOT GLOB '*[^0-9a-f]*'),
		policy_digest TEXT NOT NULL CHECK(length(policy_digest)=64 AND policy_digest NOT GLOB '*[^0-9a-f]*'),
		source_task_id TEXT NOT NULL REFERENCES task_heads(task_id),
		source_session_id TEXT NOT NULL CHECK(length(CAST(source_session_id AS BLOB)) BETWEEN 1 AND 128),
		source_turn_id TEXT NOT NULL CHECK(length(CAST(source_turn_id AS BLOB)) BETWEEN 1 AND 128),
		source_attempt_id TEXT NOT NULL CHECK(length(CAST(source_attempt_id AS BLOB)) BETWEEN 1 AND 128),
		source_completion_event_id TEXT NOT NULL UNIQUE REFERENCES events(id),
		source_completion_sequence INTEGER NOT NULL CHECK(source_completion_sequence>0),
		source_completion_digest TEXT NOT NULL CHECK(length(source_completion_digest)=64 AND source_completion_digest NOT GLOB '*[^0-9a-f]*'),
		source_output_digest TEXT NOT NULL CHECK(length(source_output_digest)=64 AND source_output_digest NOT GLOB '*[^0-9a-f]*'),
		source_terminal_event_id TEXT NOT NULL UNIQUE REFERENCES events(id),
		source_terminal_sequence INTEGER NOT NULL CHECK(source_terminal_sequence>source_completion_sequence),
		source_terminal_digest TEXT NOT NULL CHECK(length(source_terminal_digest)=64 AND source_terminal_digest NOT GLOB '*[^0-9a-f]*'),
		source_domain TEXT NOT NULL CHECK(length(CAST(source_domain AS BLOB)) BETWEEN 1 AND 128),
		source_profile TEXT NOT NULL CHECK(length(CAST(source_profile AS BLOB)) BETWEEN 1 AND 128),
		source_privacy TEXT NOT NULL CHECK(length(CAST(source_privacy AS BLOB)) BETWEEN 1 AND 128),
		source_admission_id TEXT NOT NULL UNIQUE REFERENCES workboard_execution_admissions(admission_id),
		source_admission_digest TEXT NOT NULL CHECK(length(source_admission_digest)=64 AND source_admission_digest NOT GLOB '*[^0-9a-f]*'),
		source_model_id TEXT NOT NULL CHECK(length(CAST(source_model_id AS BLOB)) BETWEEN 1 AND 512),
		source_provider_id TEXT NOT NULL CHECK(length(CAST(source_provider_id AS BLOB)) BETWEEN 1 AND 128),
		source_config_id TEXT NOT NULL CHECK(length(source_config_id)=64 AND source_config_id NOT GLOB '*[^0-9a-f]*'),
		reviewer_id TEXT NOT NULL CHECK(length(CAST(reviewer_id AS BLOB)) BETWEEN 1 AND 128),
		reviewer_model_id TEXT NOT NULL CHECK(length(CAST(reviewer_model_id AS BLOB)) BETWEEN 1 AND 512),
		reviewer_provider_id TEXT NOT NULL CHECK(length(CAST(reviewer_provider_id AS BLOB)) BETWEEN 1 AND 128),
		reviewer_config_id TEXT NOT NULL CHECK(length(reviewer_config_id)=64 AND reviewer_config_id NOT GLOB '*[^0-9a-f]*'),
		audit_id TEXT NOT NULL UNIQUE REFERENCES audit_records(id),
		audit_digest TEXT NOT NULL UNIQUE CHECK(length(audit_digest)=64 AND audit_digest NOT GLOB '*[^0-9a-f]*'),
		evidence_digest TEXT NOT NULL CHECK(length(evidence_digest)=64 AND evidence_digest NOT GLOB '*[^0-9a-f]*'),
		evidence_count INTEGER NOT NULL CHECK(evidence_count BETWEEN 0 AND 100),
		settlement_digest TEXT NOT NULL UNIQUE CHECK(length(settlement_digest)=64 AND settlement_digest NOT GLOB '*[^0-9a-f]*'),
		recorded_at INTEGER NOT NULL CHECK(recorded_at>=0),
		outcome_digest TEXT NOT NULL UNIQUE CHECK(length(outcome_digest)=64 AND outcome_digest NOT GLOB '*[^0-9a-f]*'),
		body BLOB NOT NULL CHECK(length(body) BETWEEN 1 AND 1048576));
	CREATE INDEX workboard_auxiliary_review_outcomes_board
		ON workboard_auxiliary_review_outcomes(board_id,card_id,recorded_at,outcome_id);
	CREATE INDEX workboard_auxiliary_review_outcomes_source
		ON workboard_auxiliary_review_outcomes(source_task_id,source_terminal_sequence,outcome_id);
	CREATE TRIGGER workboard_auxiliary_review_outcome_binding BEFORE INSERT ON workboard_auxiliary_review_outcomes
		WHEN EXISTS(SELECT 1 FROM workboard_auxiliary_review_legacy_outcome_admissions l
			WHERE l.admission_id=NEW.admission_id)
		OR NOT EXISTS(SELECT 1 FROM workboard_auxiliary_review_admissions a
			JOIN workboard_auxiliary_review_settlements s ON s.admission_id=a.admission_id
			JOIN workboard_candidates c ON c.board_id=a.board_id AND c.card_id=a.card_id
				AND c.attempt_id=a.attempt_id AND c.id=a.candidate_id
			JOIN workboard_execution_admissions x ON x.admission_id=NEW.source_admission_id
			JOIN events completion ON completion.id=NEW.source_completion_event_id
			JOIN event_log completion_log ON completion_log.event_id=completion.id
			JOIN events terminal ON terminal.id=NEW.source_terminal_event_id
			JOIN event_log terminal_log ON terminal_log.event_id=terminal.id
			JOIN audit_records r ON r.id=NEW.audit_id
			WHERE a.admission_id=NEW.admission_id AND a.admission_digest=NEW.admission_digest
			AND a.operation_id=NEW.operation_id AND a.board_id=NEW.board_id AND a.card_id=NEW.card_id
			AND a.attempt_id=NEW.attempt_id AND a.claim_id=NEW.claim_id AND a.candidate_id=NEW.candidate_id
			AND a.candidate_digest=NEW.candidate_digest AND a.criteria_digest=NEW.criteria_digest
			AND a.policy_digest=NEW.policy_digest AND a.reviewer_id=NEW.reviewer_id
			AND a.model_id=NEW.reviewer_model_id AND a.provider_id=NEW.reviewer_provider_id
			AND a.config_id=NEW.reviewer_config_id
			AND c.digest=NEW.candidate_digest AND c.criteria_digest=NEW.criteria_digest
			AND c.policy_digest=NEW.policy_digest AND c.evidence_digest=NEW.evidence_digest
			AND c.evidence_count=NEW.evidence_count
			AND s.disposition='completed' AND s.settlement_digest=NEW.settlement_digest
			AND s.admission_digest=NEW.admission_digest AND NEW.recorded_at>=s.settled_at
			AND x.task_id=NEW.source_task_id AND x.session_id=NEW.source_session_id
			AND x.board_id=NEW.board_id AND x.card_id=NEW.card_id AND x.attempt_id=NEW.attempt_id
			AND x.claim_id=NEW.claim_id AND x.policy_digest=NEW.policy_digest
			AND x.admission_digest=NEW.source_admission_digest
			AND x.model_id=NEW.source_model_id AND x.provider_id=NEW.source_provider_id
			AND x.config_id=NEW.source_config_id
			AND completion.task_id=NEW.source_task_id AND completion.sequence=NEW.source_completion_sequence
			AND completion_log.task_id=NEW.source_task_id AND completion_log.task_sequence=NEW.source_completion_sequence
			AND completion_log.body_digest=NEW.source_completion_digest
			AND json_extract(completion.body,'$.session_id')=NEW.source_session_id
			AND json_extract(completion.body,'$.turn_id')=NEW.source_turn_id
			AND json_extract(completion.body,'$.attempt_id')=NEW.source_attempt_id
			AND json_extract(completion.body,'$.kind')='turn.completed'
			AND terminal.task_id=NEW.source_task_id AND terminal.sequence=NEW.source_terminal_sequence
			AND terminal_log.task_id=NEW.source_task_id AND terminal_log.task_sequence=NEW.source_terminal_sequence
			AND terminal_log.body_digest=NEW.source_terminal_digest
			AND json_extract(terminal.body,'$.session_id')=NEW.source_session_id
			AND json_extract(terminal.body,'$.turn_id')=NEW.source_turn_id
			AND json_extract(terminal.body,'$.attempt_id')=NEW.source_attempt_id
			AND json_extract(terminal.body,'$.kind')='task.completed'
			AND r.task_id=NEW.source_task_id
			AND json_extract(r.body,'$.ID')=NEW.audit_id
			AND json_extract(r.body,'$.TaskID')=NEW.source_task_id
			AND json_extract(r.body,'$.AttemptID')=NEW.source_attempt_id
			AND json_extract(r.body,'$.EvaluatorModel')=NEW.reviewer_model_id
			AND json_extract(r.body,'$.EvaluatorProvider')=NEW.reviewer_provider_id)
		BEGIN SELECT RAISE(ABORT,'workboard auxiliary review outcome binding mismatch'); END;
	CREATE TRIGGER workboard_auxiliary_review_outcome_immutable_update BEFORE UPDATE ON workboard_auxiliary_review_outcomes
		BEGIN SELECT RAISE(ABORT,'workboard auxiliary review outcome is immutable'); END;
	CREATE TRIGGER workboard_auxiliary_review_outcome_immutable_delete BEFORE DELETE ON workboard_auxiliary_review_outcomes
		BEGIN SELECT RAISE(ABORT,'workboard auxiliary review outcome is immutable'); END;
	CREATE TRIGGER workboard_auxiliary_review_settlement_charge_limit BEFORE INSERT ON workboard_auxiliary_review_settlements
		WHEN NEW.charged_time_ms>300000
		BEGIN SELECT RAISE(ABORT,'workboard auxiliary review settlement charge exceeds limit'); END;
	PRAGMA user_version=45;`); err != nil {
		return err
	}
	return validateWorkboardAuxiliaryReviewOutcomeSchema(ctx, conn)
}

// Recovery tests may lower user_version while leaving empty future tables.
// Empty tables carry no authority and are safe to discard. Any retained row
// is provenance or outcome authority and therefore fails closed.
func discardEmptyFutureAuxiliaryReviewOutcomes(ctx context.Context, conn *sql.Conn) error {
	var retained int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN
		('workboard_auxiliary_review_outcomes','workboard_auxiliary_review_legacy_outcome_admissions')`).Scan(&retained); err != nil || retained == 0 {
		return err
	}
	if retained != 2 {
		return errors.New("incomplete workboard auxiliary review outcome schema before schema 45")
	}
	var rows int
	if err := conn.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM workboard_auxiliary_review_outcomes)+
		(SELECT count(*) FROM workboard_auxiliary_review_legacy_outcome_admissions)`).Scan(&rows); err != nil {
		return err
	}
	if rows != 0 {
		return errors.New("workboard auxiliary review outcomes exist before schema 45")
	}
	_, err := conn.ExecContext(ctx, `DROP TRIGGER IF EXISTS workboard_auxiliary_review_settlement_charge_limit;
		DROP TABLE workboard_auxiliary_review_outcomes;
		DROP TABLE workboard_auxiliary_review_legacy_outcome_admissions`)
	return err
}

func validateWorkboardAuxiliaryReviewOutcomeSchema(ctx context.Context, conn *sql.Conn) error {
	if err := validateWorkboardAuxiliaryReviewSuccessorFenceSchema(ctx, conn); err != nil {
		return err
	}
	if !browserTableShape(ctx, conn, "workboard_auxiliary_review_legacy_outcome_admissions",
		"admission_id:TEXT:0:1,admission_digest:TEXT:1:0") ||
		!browserTableRules(ctx, conn, "workboard_auxiliary_review_legacy_outcome_admissions", []string{
			"admission_idtextprimarykeyreferencesworkboard_auxiliary_review_admissions(admission_id)",
			"admission_digesttextnotnullunique",
		}) {
		return errors.New("invalid workboard auxiliary review legacy outcome admission table")
	}
	if !browserTableShape(ctx, conn, "workboard_auxiliary_review_outcomes",
		"outcome_id:TEXT:0:1,version:INTEGER:1:0,admission_id:TEXT:1:0,admission_digest:TEXT:1:0,operation_id:TEXT:1:0,board_id:TEXT:1:0,card_id:TEXT:1:0,attempt_id:TEXT:1:0,claim_id:TEXT:1:0,candidate_id:TEXT:1:0,candidate_digest:TEXT:1:0,criteria_digest:TEXT:1:0,policy_digest:TEXT:1:0,source_task_id:TEXT:1:0,source_session_id:TEXT:1:0,source_turn_id:TEXT:1:0,source_attempt_id:TEXT:1:0,source_completion_event_id:TEXT:1:0,source_completion_sequence:INTEGER:1:0,source_completion_digest:TEXT:1:0,source_output_digest:TEXT:1:0,source_terminal_event_id:TEXT:1:0,source_terminal_sequence:INTEGER:1:0,source_terminal_digest:TEXT:1:0,source_domain:TEXT:1:0,source_profile:TEXT:1:0,source_privacy:TEXT:1:0,source_admission_id:TEXT:1:0,source_admission_digest:TEXT:1:0,source_model_id:TEXT:1:0,source_provider_id:TEXT:1:0,source_config_id:TEXT:1:0,reviewer_id:TEXT:1:0,reviewer_model_id:TEXT:1:0,reviewer_provider_id:TEXT:1:0,reviewer_config_id:TEXT:1:0,audit_id:TEXT:1:0,audit_digest:TEXT:1:0,evidence_digest:TEXT:1:0,evidence_count:INTEGER:1:0,settlement_digest:TEXT:1:0,recorded_at:INTEGER:1:0,outcome_digest:TEXT:1:0,body:BLOB:1:0") ||
		!browserTableRules(ctx, conn, "workboard_auxiliary_review_outcomes", []string{
			"versionintegernotnullcheck(version=1)",
			"admission_idtextnotnulluniquereferencesworkboard_auxiliary_review_admissions(admission_id)",
			"audit_idtextnotnulluniquereferencesaudit_records(id)",
			"source_terminal_sequenceintegernotnullcheck(source_terminal_sequence>source_completion_sequence)",
			"outcome_digesttextnotnullunique",
		}) {
		return errors.New("invalid workboard auxiliary review outcome table")
	}
	for name, rules := range map[string][]string{
		"workboard_auxiliary_review_outcomes_board":  {"onworkboard_auxiliary_review_outcomes(board_id,card_id,recorded_at,outcome_id)"},
		"workboard_auxiliary_review_outcomes_source": {"onworkboard_auxiliary_review_outcomes(source_task_id,source_terminal_sequence,outcome_id)"},
	} {
		if !workboardObjectRules(ctx, conn, "index", name, rules) {
			return errors.New("invalid " + name)
		}
	}
	for name, rules := range map[string][]string{
		"workboard_auxiliary_review_legacy_outcome_admission_sealed_insert":    {"beforeinsertonworkboard_auxiliary_review_legacy_outcome_admissions", "migrationsealed"},
		"workboard_auxiliary_review_legacy_outcome_admission_immutable_update": {"beforeupdateonworkboard_auxiliary_review_legacy_outcome_admissions", "outcomeadmissionisimmutable"},
		"workboard_auxiliary_review_legacy_outcome_admission_immutable_delete": {"beforedeleteonworkboard_auxiliary_review_legacy_outcome_admissions", "outcomeadmissionisimmutable"},
		"workboard_auxiliary_review_outcome_binding": {
			"beforeinsertonworkboard_auxiliary_review_outcomes", "l.admission_id=new.admission_id",
			"a.admission_digest=new.admission_digest", "a.operation_id=new.operation_id",
			"a.candidate_digest=new.candidate_digest", "a.reviewer_id=new.reviewer_id",
			"a.model_id=new.reviewer_model_id", "a.provider_id=new.reviewer_provider_id",
			"a.config_id=new.reviewer_config_id", "s.disposition='completed'",
			"s.settlement_digest=new.settlement_digest", "c.evidence_digest=new.evidence_digest",
			"x.admission_digest=new.source_admission_digest", "x.model_id=new.source_model_id",
			"x.provider_id=new.source_provider_id", "x.config_id=new.source_config_id",
			"completion.id=new.source_completion_event_id", "completion_log.body_digest=new.source_completion_digest",
			"json_extract(completion.body,'$.kind')='turn.completed'",
			"terminal.id=new.source_terminal_event_id", "terminal_log.body_digest=new.source_terminal_digest",
			"json_extract(terminal.body,'$.kind')='task.completed'", "r.task_id=new.source_task_id",
			"json_extract(r.body,'$.attemptid')=new.source_attempt_id",
			"json_extract(r.body,'$.evaluatormodel')=new.reviewer_model_id",
			"json_extract(r.body,'$.evaluatorprovider')=new.reviewer_provider_id", "outcomebindingmismatch",
		},
		"workboard_auxiliary_review_outcome_immutable_update": {"beforeupdateonworkboard_auxiliary_review_outcomes", "outcomeisimmutable"},
		"workboard_auxiliary_review_outcome_immutable_delete": {"beforedeleteonworkboard_auxiliary_review_outcomes", "outcomeisimmutable"},
		"workboard_auxiliary_review_settlement_charge_limit":  {"beforeinsertonworkboard_auxiliary_review_settlements", "new.charged_time_ms>300000", "settlementchargeexceedslimit"},
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
