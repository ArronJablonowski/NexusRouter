package telemetry

import (
	"context"
	"database/sql"
	"errors"
)

// Schema 46 adds restart-safe auxiliary intent-classification attempts. A
// classification is allowed to precede task_heads creation, so task_id is an
// intentionally unreferenced but unique correlation identity. submission_id is
// optional; when present its submission binding is also unique.
func migrateIntentClassificationAttempts(ctx context.Context, conn *sql.Conn) error {
	if err := validateWorkboardAuxiliaryReviewOutcomeSchema(ctx, conn); err != nil {
		return err
	}
	var retained int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master
		WHERE type IN('table','index') AND name IN
		('intent_classification_attempts','intent_classification_attempts_submission',
		'intent_classification_attempts_task')`).Scan(&retained); err != nil {
		return err
	}
	if retained != 0 {
		return errors.New("intent classification attempt schema exists before schema 46")
	}
	if _, err := conn.ExecContext(ctx, `CREATE TABLE intent_classification_attempts(
		id TEXT PRIMARY KEY CHECK(length(CAST(id AS BLOB)) BETWEEN 1 AND 128),
		task_id TEXT NOT NULL CHECK(length(CAST(task_id AS BLOB)) BETWEEN 1 AND 128),
		session_id TEXT NOT NULL CHECK(length(CAST(session_id AS BLOB)) BETWEEN 1 AND 128),
		submission_id TEXT NOT NULL CHECK(length(CAST(submission_id AS BLOB))<=128),
		status TEXT NOT NULL CHECK(status IN('started','completed','failed','canceled')),
		body BLOB NOT NULL CHECK(length(body) BETWEEN 1 AND 1048576));
	CREATE UNIQUE INDEX intent_classification_attempts_submission
		ON intent_classification_attempts(submission_id) WHERE submission_id<>'';
	CREATE UNIQUE INDEX intent_classification_attempts_task
		ON intent_classification_attempts(task_id);
	PRAGMA user_version=46;`); err != nil {
		return err
	}
	return validateIntentClassificationAttemptSchema(ctx, conn)
}

// Tests and recovery rehearsals deliberately lower user_version while leaving
// later empty objects in place. Empty schema-46 objects carry no classification
// authority and may be recreated. Any retained attempt is durable evidence and
// therefore makes a claimed pre-46 database fail closed.
func discardEmptyFutureIntentClassificationAttempts(ctx context.Context, conn *sql.Conn) error {
	var tables, submissionIndexes, taskIndexes int
	if err := conn.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM sqlite_master WHERE type='table' AND name='intent_classification_attempts'),
		(SELECT count(*) FROM sqlite_master WHERE type='index' AND name='intent_classification_attempts_submission'),
		(SELECT count(*) FROM sqlite_master WHERE type='index' AND name='intent_classification_attempts_task')`).
		Scan(&tables, &submissionIndexes, &taskIndexes); err != nil {
		return err
	}
	if tables == 0 && submissionIndexes == 0 && taskIndexes == 0 {
		return nil
	}
	if tables != 1 || submissionIndexes != 1 || taskIndexes != 1 {
		return errors.New("incomplete intent classification attempt schema before schema 46")
	}
	var attempts int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM intent_classification_attempts`).Scan(&attempts); err != nil {
		return err
	}
	if attempts != 0 {
		return errors.New("intent classification attempts exist before schema 46")
	}
	_, err := conn.ExecContext(ctx, `DROP TABLE intent_classification_attempts`)
	return err
}

func validateIntentClassificationAttemptSchema(ctx context.Context, conn *sql.Conn) error {
	if err := validateWorkboardAuxiliaryReviewOutcomeSchema(ctx, conn); err != nil {
		return err
	}
	if !browserTableShape(ctx, conn, "intent_classification_attempts",
		"id:TEXT:0:1,task_id:TEXT:1:0,session_id:TEXT:1:0,submission_id:TEXT:1:0,status:TEXT:1:0,body:BLOB:1:0") ||
		!browserTableRules(ctx, conn, "intent_classification_attempts", []string{
			"task_idtextnotnullcheck(length(cast(task_idasblob))between1and128)",
			"session_idtextnotnullcheck(length(cast(session_idasblob))between1and128)",
			"submission_idtextnotnullcheck(length(cast(submission_idasblob))<=128)",
			"statustextnotnullcheck(statusin('started','completed','failed','canceled'))",
		}) {
		return errors.New("invalid intent classification attempt table")
	}
	if !workboardObjectRules(ctx, conn, "index", "intent_classification_attempts_submission", []string{
		"uniqueindexintent_classification_attempts_submissiononintent_classification_attempts(submission_id)wheresubmission_id<>''",
	}) || !workboardObjectRules(ctx, conn, "index", "intent_classification_attempts_task", []string{
		"uniqueindexintent_classification_attempts_taskonintent_classification_attempts(task_id)",
	}) {
		return errors.New("invalid intent classification attempt index")
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
