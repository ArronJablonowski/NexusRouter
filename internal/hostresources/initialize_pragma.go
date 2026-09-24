package hostresources

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// SQLite can reject concurrent journal-mode initialization immediately, without
// invoking its busy handler. Retry only that idempotent pragma, before starting
// any admission or migration transaction.
func initializePragma(ctx context.Context, db *sql.DB, query string) error {
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, err := db.ExecContext(ctx, query)
		var coded interface{ Code() int }
		if err == nil || query != "PRAGMA journal_mode=WAL" || !errors.As(err, &coded) || coded.Code()&255 != 5 || time.Now().After(deadline) {
			return err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
