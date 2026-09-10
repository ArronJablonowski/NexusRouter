package telemetry

import (
	"context"
	"database/sql"
	"errors"
)

// Schema 41 records only composite admissions created by the atomic API. Older
// adjacent runtime and claim records are intentionally not backfilled because
// their shared transaction boundary cannot be proved after the fact.
func migrateTaskStartClaims(ctx context.Context, conn *sql.Conn) error {
	var retained int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='workboard_task_start_claims'`).Scan(&retained); err != nil {
		return err
	}
	if retained != 0 {
		return errors.New("task start claim table exists before schema 41")
	}
	if err := validateWorkboardSchema40(ctx, conn); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `CREATE TABLE workboard_task_start_claims(
		task_id TEXT PRIMARY KEY REFERENCES task_heads(task_id),
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
		expected_card_revision INTEGER NOT NULL CHECK(expected_card_revision>0),
		policy_digest TEXT NOT NULL CHECK(length(policy_digest)=64 AND policy_digest NOT GLOB '*[^0-9a-f]*'),
		lease_ttl_ns INTEGER NOT NULL CHECK(lease_ttl_ns BETWEEN 1000000 AND 600000000000),
		created_at INTEGER NOT NULL CHECK(created_at>=0),
		body BLOB NOT NULL CHECK(length(body) BETWEEN 1 AND 16384),
		FOREIGN KEY(board_id,operation_id) REFERENCES workboard_operations(board_id,operation_id),
		FOREIGN KEY(board_id,card_id,attempt_id,claim_id) REFERENCES workboard_claims(board_id,card_id,attempt_id,id));
	CREATE INDEX workboard_task_start_claims_board ON workboard_task_start_claims(board_id,card_id,created_at,task_id);
	CREATE TRIGGER workboard_task_start_claim_immutable_update BEFORE UPDATE ON workboard_task_start_claims
		BEGIN SELECT RAISE(ABORT,'workboard task start claim is immutable'); END;
	CREATE TRIGGER workboard_task_start_claim_immutable_delete BEFORE DELETE ON workboard_task_start_claims
		BEGIN SELECT RAISE(ABORT,'workboard task start claim is immutable'); END;
	PRAGMA user_version=41;`); err != nil {
		return err
	}
	return validateTaskStartClaimSchema(ctx, conn)
}

func validateTaskStartClaimSchema(ctx context.Context, conn *sql.Conn) error {
	if !browserTableShape(ctx, conn, "workboard_task_start_claims",
		"task_id:TEXT:0:1,session_id:TEXT:1:0,event_id:TEXT:1:0,event_digest:TEXT:1:0,board_id:TEXT:1:0,card_id:TEXT:1:0,attempt_id:TEXT:1:0,claim_id:TEXT:1:0,worker_id:TEXT:1:0,operation_id:TEXT:1:0,request_digest:TEXT:1:0,expected_card_revision:INTEGER:1:0,policy_digest:TEXT:1:0,lease_ttl_ns:INTEGER:1:0,created_at:INTEGER:1:0,body:BLOB:1:0") {
		return errors.New("invalid task start claim table shape")
	}
	if !browserTableRules(ctx, conn, "workboard_task_start_claims", []string{
		"task_idtextprimarykeyreferences", "event_idtextnotnulluniquereferences", "operation_idtextnotnullunique",
		"check(lease_ttl_nsbetween1000000and600000000000)",
		"foreignkey(board_id,operation_id)referencesworkboard_operations(board_id,operation_id)",
		"foreignkey(board_id,card_id,attempt_id,claim_id)referencesworkboard_claims(board_id,card_id,attempt_id,id)",
	}) {
		return errors.New("invalid task start claim table rules")
	}
	if !workboardObjectRules(ctx, conn, "index", "workboard_task_start_claims_board", []string{"onworkboard_task_start_claims(board_id,card_id,created_at,task_id)"}) {
		return errors.New("invalid task start claim index")
	}
	if !workboardObjectRules(ctx, conn, "trigger", "workboard_task_start_claim_immutable_update", []string{"beforeupdateonworkboard_task_start_claims", "raise(abort,'workboardtaskstartclaimisimmutable')"}) ||
		!workboardObjectRules(ctx, conn, "trigger", "workboard_task_start_claim_immutable_delete", []string{"beforedeleteonworkboard_task_start_claims", "raise(abort,'workboardtaskstartclaimisimmutable')"}) {
		return errors.New("invalid task start claim immutability")
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
