package telemetry

import (
	"context"
	"database/sql"

	"github.com/ArronJablonowski/DarwinRouter/internal/stateschema"
)

// beginSchemaTransaction validates current schemas in a WAL read snapshot so
// large histories cannot block lease heartbeats or event writers. Migrations
// retain their exclusive writer reservation and reread the version under it.
func beginSchemaTransaction(ctx context.Context, conn *sql.Conn) (int, error) {
	if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
		return 0, err
	}
	var version int
	if err := conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return 0, err
	}
	if version < stateschema.Current {
		if _, err := conn.ExecContext(ctx, "ROLLBACK"); err != nil {
			return 0, err
		}
		if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
			return 0, err
		}
		if err := conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
			return 0, err
		}
	}
	return version, nil
}
