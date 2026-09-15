package telemetry

import (
	"context"
	"database/sql"
	"errors"
)

// Schema 47 adds an immutable, redaction-safe lifecycle journal for automatic
// outcome supervision. The journal is deliberately separate from task events:
// it cannot contain prompts, model output, tool arguments, or error text.
func migrateOutcomeSupervisionEvents(ctx context.Context, conn *sql.Conn) error {
	if err := validateIntentClassificationAttemptSchema(ctx, conn); err != nil {
		return err
	}
	var retained int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master
		WHERE type IN('table','index') AND name IN
		('outcome_supervision_events','outcome_supervision_events_operation')`).Scan(&retained); err != nil {
		return err
	}
	if retained != 0 {
		return errors.New("outcome supervision event schema exists before schema 47")
	}
	if _, err := conn.ExecContext(ctx, `CREATE TABLE outcome_supervision_events(
		id TEXT PRIMARY KEY CHECK(length(CAST(id AS BLOB))=64),
		operation_id TEXT NOT NULL CHECK(length(CAST(operation_id AS BLOB))=64),
		check_id TEXT NOT NULL CHECK(length(CAST(check_id AS BLOB))=32),
		sequence INTEGER NOT NULL CHECK(sequence BETWEEN 1 AND 1000000000),
		code TEXT NOT NULL CHECK(code IN('waiting','ready','no_action','rolled_back','error')),
		skill_scope TEXT NOT NULL CHECK(length(CAST(skill_scope AS BLOB)) BETWEEN 1 AND 64),
		skill_name TEXT NOT NULL CHECK(length(CAST(skill_name AS BLOB)) BETWEEN 1 AND 64),
		activation_id TEXT NOT NULL CHECK(length(CAST(activation_id AS BLOB))=32),
		activation_revision TEXT NOT NULL CHECK(length(CAST(activation_revision AS BLOB))=64),
		policy_id TEXT NOT NULL CHECK(length(CAST(policy_id AS BLOB))=64),
		recorded_at INTEGER NOT NULL,
		body BLOB NOT NULL CHECK(length(body) BETWEEN 1 AND 4096),
		UNIQUE(operation_id,sequence),
		UNIQUE(operation_id,check_id,code));
	CREATE INDEX outcome_supervision_events_operation
		ON outcome_supervision_events(operation_id,sequence);
	PRAGMA user_version=47;`); err != nil {
		return err
	}
	return validateOutcomeSupervisionEventSchema(ctx, conn)
}

// Tests and recovery rehearsals may lower user_version while leaving empty
// future objects. Empty objects grant no authority and can be rebuilt; retained
// events make the claimed older database fail closed.
func discardEmptyFutureOutcomeSupervisionEvents(ctx context.Context, conn *sql.Conn) error {
	var tables, indexes int
	if err := conn.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM sqlite_master WHERE type='table' AND name='outcome_supervision_events'),
		(SELECT count(*) FROM sqlite_master WHERE type='index' AND name='outcome_supervision_events_operation')`).Scan(&tables, &indexes); err != nil {
		return err
	}
	if tables == 0 && indexes == 0 {
		return nil
	}
	if tables != 1 || indexes != 1 {
		return errors.New("incomplete outcome supervision event schema before schema 47")
	}
	var events int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM outcome_supervision_events`).Scan(&events); err != nil {
		return err
	}
	if events != 0 {
		return errors.New("outcome supervision events exist before schema 47")
	}
	_, err := conn.ExecContext(ctx, `DROP TABLE outcome_supervision_events`)
	return err
}

func validateOutcomeSupervisionEventSchema(ctx context.Context, conn *sql.Conn) error {
	if err := validateIntentClassificationAttemptSchema(ctx, conn); err != nil {
		return err
	}
	if !browserTableShape(ctx, conn, "outcome_supervision_events",
		"id:TEXT:0:1,operation_id:TEXT:1:0,check_id:TEXT:1:0,sequence:INTEGER:1:0,code:TEXT:1:0,skill_scope:TEXT:1:0,skill_name:TEXT:1:0,activation_id:TEXT:1:0,activation_revision:TEXT:1:0,policy_id:TEXT:1:0,recorded_at:INTEGER:1:0,body:BLOB:1:0") ||
		!browserTableRules(ctx, conn, "outcome_supervision_events", []string{
			"operation_idtextnotnullcheck(length(cast(operation_idasblob))=64)",
			"check_idtextnotnullcheck(length(cast(check_idasblob))=32)",
			"sequenceintegernotnullcheck(sequencebetween1and1000000000)",
			"codetextnotnullcheck(codein('waiting','ready','no_action','rolled_back','error'))",
			"skill_scopetextnotnullcheck(length(cast(skill_scopeasblob))between1and64)",
			"skill_nametextnotnullcheck(length(cast(skill_nameasblob))between1and64)",
			"activation_idtextnotnullcheck(length(cast(activation_idasblob))=32)",
			"activation_revisiontextnotnullcheck(length(cast(activation_revisionasblob))=64)",
			"policy_idtextnotnullcheck(length(cast(policy_idasblob))=64)",
			"unique(operation_id,sequence)",
			"unique(operation_id,check_id,code)",
		}) || !workboardObjectRules(ctx, conn, "index", "outcome_supervision_events_operation", []string{
		"indexoutcome_supervision_events_operationonoutcome_supervision_events(operation_id,sequence)",
	}) {
		return errors.New("invalid outcome supervision event schema")
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
