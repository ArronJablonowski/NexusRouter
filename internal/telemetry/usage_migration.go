package telemetry

import (
	"context"
	"database/sql"
	"errors"
)

var errUsageSchema = errors.New("invalid usage accounting schema")

func migrateUsageAccounting(ctx context.Context, conn *sql.Conn) error {
	var present int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name IN(
	 'usage_metadata','usage_records','usage_records_task','usage_records_session','usage_heads','usage_corrections','usage_corrections_base')`).Scan(&present); err != nil {
		return err
	}
	if present != 0 {
		return errUsageSchema
	}
	_, err := conn.ExecContext(ctx, `CREATE TABLE usage_metadata(
 singleton INTEGER PRIMARY KEY CHECK(singleton=1),version INTEGER NOT NULL CHECK(version=1),started_at TEXT NOT NULL);
	INSERT INTO usage_metadata(singleton,version,started_at) VALUES(1,1,strftime('%Y-%m-%dT%H:%M:%fZ','now'));
	CREATE TABLE usage_records(
 id TEXT PRIMARY KEY,task_id TEXT NOT NULL REFERENCES task_heads(task_id),session_id TEXT NOT NULL,
	operation_id TEXT NOT NULL,
 role TEXT NOT NULL CHECK(role IN('primary_execution','fallback','classifier','summarizer','orchestrator_audit','optional_judge')),
	 evidence_kind TEXT NOT NULL CHECK(evidence_kind IN('event','review','audit','summary')),evidence_id TEXT NOT NULL,body BLOB NOT NULL,
	 UNIQUE(task_id,role,operation_id));
 CREATE INDEX usage_records_task ON usage_records(task_id,id);
 CREATE INDEX usage_records_session ON usage_records(session_id,id);
 CREATE TABLE usage_heads(base_id TEXT PRIMARY KEY REFERENCES usage_records(id),current_id TEXT NOT NULL UNIQUE);
 CREATE TABLE usage_corrections(
 id TEXT PRIMARY KEY,base_id TEXT NOT NULL REFERENCES usage_records(id),supersedes TEXT NOT NULL UNIQUE,
 task_id TEXT NOT NULL REFERENCES task_heads(task_id),session_id TEXT NOT NULL,role TEXT NOT NULL,
 evidence_kind TEXT NOT NULL,evidence_id TEXT NOT NULL,body BLOB NOT NULL);
 CREATE INDEX usage_corrections_base ON usage_corrections(base_id,id);
 PRAGMA user_version=30;`)
	return err
}
