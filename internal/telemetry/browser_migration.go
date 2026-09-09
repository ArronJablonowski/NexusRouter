package telemetry

import (
	"context"
	"database/sql"
	"strings"
)

func migrateBrowserOperations(ctx context.Context, conn *sql.Conn) error {
	_, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS browser_operations(
	 operation_id TEXT PRIMARY KEY,session_subject TEXT NOT NULL,key_digest TEXT NOT NULL,
	 kind TEXT NOT NULL,request_digest TEXT NOT NULL,state TEXT NOT NULL CHECK(state IN('pending','committed','rejected')),
	 response BLOB,created_at INTEGER NOT NULL,updated_at INTEGER NOT NULL,
	 UNIQUE(session_subject,key_digest));
	 CREATE INDEX IF NOT EXISTS browser_operations_subject_created ON browser_operations(session_subject,created_at DESC,operation_id DESC);
	 CREATE INDEX IF NOT EXISTS browser_operations_committed_updated ON browser_operations(state,updated_at);
	 CREATE TABLE IF NOT EXISTS browser_feedback(
	 id TEXT PRIMARY KEY,task_id TEXT NOT NULL REFERENCES task_heads(task_id),supersedes TEXT UNIQUE,
	 accepted INTEGER NOT NULL CHECK(accepted IN(0,1)),attempt_cost REAL NOT NULL CHECK(attempt_cost>=0),
	 created_at INTEGER NOT NULL,body BLOB NOT NULL);
	 CREATE INDEX IF NOT EXISTS browser_feedback_task_created ON browser_feedback(task_id,created_at,id);`)
	if err != nil || !browserTableShape(ctx, conn, "browser_operations", "operation_id:TEXT:0:1,session_subject:TEXT:1:0,key_digest:TEXT:1:0,kind:TEXT:1:0,request_digest:TEXT:1:0,state:TEXT:1:0,response:BLOB:0:0,created_at:INTEGER:1:0,updated_at:INTEGER:1:0") ||
		!browserTableShape(ctx, conn, "browser_feedback", "id:TEXT:0:1,task_id:TEXT:1:0,supersedes:TEXT:0:0,accepted:INTEGER:1:0,attempt_cost:REAL:1:0,created_at:INTEGER:1:0,body:BLOB:1:0") ||
		!browserTableRules(ctx, conn, "browser_operations", []string{"unique(session_subject,key_digest)", "check(statein('pending','committed','rejected'))"}) ||
		!browserTableRules(ctx, conn, "browser_feedback", []string{"references task_heads(task_id)", "supersedes text unique", "check(acceptedin(0,1))", "check(attempt_cost>=0)"}) {
		if err != nil {
			return err
		}
		return sql.ErrNoRows
	}
	_, err = conn.ExecContext(ctx, "PRAGMA user_version=34")
	return err
}

func browserTableRules(ctx context.Context, conn *sql.Conn, table string, expected []string) bool {
	var definition string
	if err := conn.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&definition); err != nil {
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

func browserTableShape(ctx context.Context, conn *sql.Conn, table, expected string) bool {
	var actual string
	err := conn.QueryRowContext(ctx, `SELECT group_concat(name||':'||type||':'||"notnull"||':'||pk,',') FROM pragma_table_info(?)`, table).Scan(&actual)
	return err == nil && actual == expected
}
