package telemetry

import (
	"context"
	"database/sql"
)

// Child readers are observed, never released by the worker transaction. Each
// must belong to the exact retained, unlocked execution image of the worker.
func orphanToolReaders(ctx context.Context, tx *sql.Tx, child string, worker recoveryLease) ([]recoveryLease, error) {
	rows, err := tx.QueryContext(ctx, `SELECT CASE WHEN length(CAST(token AS BLOB)) BETWEEN 1 AND 512 THEN token END FROM resource_leases WHERE task_id=? AND (released!=1 OR released IS NULL) ORDER BY token LIMIT 65`, child)
	if err != nil {
		return nil, ErrLeaseRecovery
	}
	var tokens []string
	for rows.Next() {
		var token string
		if rows.Scan(&token) != nil || len(tokens) >= 64 {
			rows.Close()
			return nil, ErrLeaseRecovery
		}
		tokens = append(tokens, token)
	}
	if rows.Err() != nil || rows.Close() != nil {
		return nil, ErrLeaseRecovery
	}
	out := make([]recoveryLease, 0, len(tokens))
	for _, token := range tokens {
		c, err := readRecoveryLease(ctx, tx, token)
		if err != nil || c.Task != child || c.Released != 0 || c.Writer != 0 || c.Process == "" || c.Process != worker.Process || c.Reference != worker.Reference {
			return nil, ErrLeaseRecovery
		}
		out = append(out, c)
	}
	return out, nil
}
func sameOrphanToolReaders(a, b []recoveryLease) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
