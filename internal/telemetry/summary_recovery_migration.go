package telemetry

import (
	"context"
	"database/sql"
	"errors"
)

// Schema 48 binds every newly started summary attempt to the exact guarded
// local process image that may finish it. An immutable, redaction-safe receipt
// records conservative interruption after independently proving owner exit.
func migrateSummaryAttemptRecoveries(ctx context.Context, conn *sql.Conn) error {
	if err := validateOutcomeSupervisionEventSchema(ctx, conn); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `ALTER TABLE summary_attempts
		ADD COLUMN process_id TEXT;
	CREATE TABLE summary_attempt_recoveries(
		id TEXT PRIMARY KEY CHECK(length(CAST(id AS BLOB))=64),
		attempt_id TEXT NOT NULL UNIQUE REFERENCES summary_attempts(id),
		task_id TEXT NOT NULL REFERENCES task_heads(task_id),
		recovered_at INTEGER NOT NULL,
		body BLOB NOT NULL CHECK(length(body) BETWEEN 1 AND 4096));
	CREATE INDEX summary_attempt_recoveries_task
		ON summary_attempt_recoveries(task_id,id);
	CREATE TRIGGER summary_attempt_process_required BEFORE INSERT ON summary_attempts
		WHEN NEW.process_id IS NULL OR NOT EXISTS(SELECT 1 FROM lease_processes WHERE id=NEW.process_id)
		BEGIN SELECT RAISE(ABORT,'summary attempt process required'); END;
	CREATE TRIGGER summary_attempt_process_immutable BEFORE UPDATE OF process_id ON summary_attempts
		WHEN NEW.process_id IS NULL OR NEW.process_id IS NOT OLD.process_id
		BEGIN SELECT RAISE(ABORT,'summary attempt process immutable'); END;
	CREATE TRIGGER summary_attempt_recovery_immutable_update BEFORE UPDATE ON summary_attempt_recoveries
		BEGIN SELECT RAISE(ABORT,'summary recovery immutable'); END;
	CREATE TRIGGER summary_attempt_recovery_immutable_delete BEFORE DELETE ON summary_attempt_recoveries
		BEGIN SELECT RAISE(ABORT,'summary recovery immutable'); END;
	CREATE TRIGGER summary_attempt_recovery_binding BEFORE INSERT ON summary_attempt_recoveries
		WHEN NOT EXISTS(SELECT 1 FROM summary_attempts a WHERE a.id=NEW.attempt_id
			AND a.task_id=NEW.task_id AND json_extract(a.body,'$.Status')='interrupted'
			AND json_extract(a.body,'$.Code')='owner_interrupted'
			AND json_extract(NEW.body,'$.ID')=NEW.id
			AND json_extract(NEW.body,'$.AttemptID')=NEW.attempt_id
			AND json_extract(NEW.body,'$.TaskID')=NEW.task_id
			AND json_extract(NEW.body,'$.State')='interrupted'
			AND json_extract(NEW.body,'$.Code')='owner_interrupted')
		BEGIN SELECT RAISE(ABORT,'summary recovery binding'); END;
	PRAGMA user_version=48;`); err != nil {
		return err
	}
	return validateSummaryAttemptRecoverySchema(ctx, conn)
}

// A lowered user_version must not reinterpret durable schema-48 ownership.
// Empty test/rehearsal remnants can be removed and rebuilt deterministically.
func discardEmptyFutureSummaryAttemptRecoveries(ctx context.Context, conn *sql.Conn) error {
	var processColumn, objects int
	rows, err := conn.QueryContext(ctx, `PRAGMA table_info(summary_attempts)`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var cid, notNull, pk int
		var name, kind string
		var defaultValue any
		if rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &pk) != nil {
			rows.Close()
			return errors.New("invalid summary attempts schema before schema 48")
		}
		if name == "process_id" {
			processColumn++
		}
	}
	if err = rows.Close(); err != nil {
		return err
	}
	if err = conn.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name IN(
		'summary_attempt_recoveries','summary_attempt_recoveries_task',
		'summary_attempt_process_required','summary_attempt_process_immutable',
		'summary_attempt_recovery_immutable_update','summary_attempt_recovery_immutable_delete',
		'summary_attempt_recovery_binding')`).Scan(&objects); err != nil {
		return err
	}
	if processColumn == 0 && objects == 0 {
		return nil
	}
	var authority int
	if processColumn == 1 {
		if err = conn.QueryRowContext(ctx, `SELECT count(*) FROM summary_attempts WHERE process_id IS NOT NULL`).Scan(&authority); err != nil {
			return err
		}
	} else if processColumn != 0 {
		return errors.New("invalid summary process ownership before schema 48")
	}
	var recoveryTable int
	if err = conn.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='summary_attempt_recoveries'`).Scan(&recoveryTable); err != nil {
		return err
	}
	if recoveryTable == 1 {
		var recoveries int
		if err = conn.QueryRowContext(ctx, `SELECT count(*) FROM summary_attempt_recoveries`).Scan(&recoveries); err != nil {
			return err
		}
		authority += recoveries
	}
	if authority != 0 {
		return errors.New("summary recovery authority exists before schema 48")
	}
	_, err = conn.ExecContext(ctx, `DROP TRIGGER IF EXISTS summary_attempt_recovery_binding;
		DROP TRIGGER IF EXISTS summary_attempt_recovery_immutable_delete;
		DROP TRIGGER IF EXISTS summary_attempt_recovery_immutable_update;
		DROP TRIGGER IF EXISTS summary_attempt_process_immutable;
		DROP TRIGGER IF EXISTS summary_attempt_process_required;
		DROP INDEX IF EXISTS summary_attempt_recoveries_task;
		DROP TABLE IF EXISTS summary_attempt_recoveries;`)
	if err == nil && processColumn == 1 {
		_, err = conn.ExecContext(ctx, `ALTER TABLE summary_attempts DROP COLUMN process_id`)
	}
	return err
}

func validateSummaryAttemptRecoverySchema(ctx context.Context, conn *sql.Conn) error {
	if err := validateOutcomeSupervisionEventSchema(ctx, conn); err != nil {
		return err
	}
	if !browserTableShape(ctx, conn, "summary_attempts", "id:TEXT:0:1,task_id:TEXT:1:0,body:BLOB:1:0,process_id:TEXT:0:0") ||
		!browserTableShape(ctx, conn, "summary_attempt_recoveries", "id:TEXT:0:1,attempt_id:TEXT:1:0,task_id:TEXT:1:0,recovered_at:INTEGER:1:0,body:BLOB:1:0") ||
		!browserTableRules(ctx, conn, "summary_attempt_recoveries", []string{
			"idtextprimarykeycheck(length(cast(idasblob))=64)",
			"attempt_idtextnotnulluniquereferencessummary_attempts(id)",
			"task_idtextnotnullreferencestask_heads(task_id)",
			"bodyblobnotnullcheck(length(body)between1and4096)",
		}) ||
		!workboardObjectRules(ctx, conn, "index", "summary_attempt_recoveries_task", []string{"indexsummary_attempt_recoveries_taskonsummary_attempt_recoveries(task_id,id)"}) ||
		!workboardObjectRules(ctx, conn, "trigger", "summary_attempt_process_required", []string{"beforeinsertonsummary_attempts", "new.process_idisnullornotexists(select1fromlease_processeswhereid=new.process_id)", "raise(abort,'summaryattemptprocessrequired')"}) ||
		!workboardObjectRules(ctx, conn, "trigger", "summary_attempt_process_immutable", []string{"beforeupdateofprocess_idonsummary_attempts", "new.process_idisnullornew.process_idisnotold.process_id"}) ||
		!workboardObjectRules(ctx, conn, "trigger", "summary_attempt_recovery_immutable_update", []string{"beforeupdateonsummary_attempt_recoveries", "raise(abort,'summaryrecoveryimmutable')"}) ||
		!workboardObjectRules(ctx, conn, "trigger", "summary_attempt_recovery_immutable_delete", []string{"beforedeleteonsummary_attempt_recoveries", "raise(abort,'summaryrecoveryimmutable')"}) ||
		!workboardObjectRules(ctx, conn, "trigger", "summary_attempt_recovery_binding", []string{"beforeinsertonsummary_attempt_recoveries", "json_extract(a.body,'$.status')='interrupted'", "json_extract(new.body,'$.id')=new.id"}) {
		return errors.New("invalid summary attempt recovery schema")
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
