package telemetry

import (
	"context"
	"database/sql"
	"errors"
)

// migrateWorkspaceIdentity creates one random, non-secret identity with the
// database. It is independent of bearer credentials and therefore survives
// daemon restarts and token rotation while remaining portable with backups.
func migrateWorkspaceIdentity(ctx context.Context, conn *sql.Conn) error {
	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS workspace_identity(
	 singleton INTEGER PRIMARY KEY CHECK(singleton=1),
	 id TEXT NOT NULL UNIQUE CHECK(length(id)=64 AND id NOT GLOB '*[^0-9a-f]*'));
	 INSERT OR IGNORE INTO workspace_identity(singleton,id) VALUES(1,lower(hex(randomblob(32))));`); err != nil {
		return err
	}
	if !browserTableShape(ctx, conn, "workspace_identity", "singleton:INTEGER:0:1,id:TEXT:1:0") ||
		!browserTableRules(ctx, conn, "workspace_identity", []string{"check(singleton=1)", "id text not null unique", "check(length(id)=64 and id not glob '*[^0-9a-f]*')"}) {
		return errors.New("invalid workspace identity schema")
	}
	var count int
	var id string
	if err := conn.QueryRowContext(ctx, `SELECT count(*),COALESCE(max(id),'') FROM workspace_identity`).Scan(&count, &id); err != nil || count != 1 || !validBrowserDigest(id) {
		if err != nil {
			return err
		}
		return errors.New("invalid workspace identity")
	}
	_, err := conn.ExecContext(ctx, "PRAGMA user_version=37")
	return err
}

// WorkspaceIdentity returns the durable non-secret identity of this database.
func (s *Store) WorkspaceIdentity(ctx context.Context) (string, error) {
	if s == nil || ctx == nil || ctx.Err() != nil {
		return "", errors.New("workspace identity unavailable")
	}
	var id string
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM workspace_identity WHERE singleton=1`).Scan(&id); err != nil || !validBrowserDigest(id) {
		if err != nil {
			return "", err
		}
		return "", errors.New("invalid workspace identity")
	}
	return id, nil
}
