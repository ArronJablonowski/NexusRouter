package telemetry

import (
	"context"
	"database/sql"
	"errors"
)

func migrateBrowserOperationRecoveries(ctx context.Context, conn *sql.Conn) error {
	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS browser_operation_recoveries(
	 operation_id TEXT PRIMARY KEY REFERENCES browser_operations(operation_id) ON DELETE CASCADE,
	 recovery_subject TEXT NOT NULL CHECK(length(recovery_subject)=64 AND recovery_subject NOT GLOB '*[^0-9a-f]*'),
	 recovered_at INTEGER NOT NULL);
	 CREATE TABLE IF NOT EXISTS legacy_browser_workboard_operations(
	 operation_id TEXT PRIMARY KEY REFERENCES browser_operations(operation_id) ON DELETE CASCADE);
	 INSERT OR IGNORE INTO legacy_browser_workboard_operations(operation_id)
	 SELECT operation_id FROM browser_operations WHERE state='pending' AND kind IN(
	 'board.create','board.revise','board.archive','card.create','card.revise','card.move','card.reorder',
	 'dependency.add','dependency.remove','card.claim','claim.heartbeat','claim.attention','checkpoint.append',
	 'candidate.submit','acceptance.accept','acceptance.reject','card.pause_request','card.cancel_request',
	 'card.cancel_finalize','card.block','card.unblock','criteria.revise','claim.recover','claim.fail');`); err != nil {
		return err
	}
	if !browserTableShape(ctx, conn, "browser_operation_recoveries", "operation_id:TEXT:0:1,recovery_subject:TEXT:1:0,recovered_at:INTEGER:1:0") ||
		!browserTableRules(ctx, conn, "browser_operation_recoveries", []string{"references browser_operations(operation_id) on delete cascade", "check(length(recovery_subject)=64 and recovery_subject not glob '*[^0-9a-f]*')"}) ||
		!browserTableShape(ctx, conn, "legacy_browser_workboard_operations", "operation_id:TEXT:0:1") ||
		!browserTableRules(ctx, conn, "legacy_browser_workboard_operations", []string{"references browser_operations(operation_id) on delete cascade"}) {
		return errors.New("invalid browser operation recovery schema")
	}
	_, err := conn.ExecContext(ctx, "PRAGMA user_version=38")
	return err
}
