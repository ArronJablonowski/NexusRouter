package telemetry

import (
	"context"
	"database/sql"
)

// migrateObservationIndex adds the key-first access path used by adaptive
// routing. The source records remain authoritative; this index stores no new
// timestamps and therefore cannot make replay or backfill arrival-dependent.
func migrateObservationIndex(ctx context.Context, conn *sql.Conn) error {
	_, err := conn.ExecContext(ctx, `CREATE INDEX evaluations_routing_key
	 ON evaluations(model,provider,domain,profile,id);
	 PRAGMA user_version=31;`)
	return err
}
