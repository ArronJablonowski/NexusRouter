package telemetry

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
)

// openExistingControl cannot create or migrate the database, even if the path
// disappears between filesystem inspection and SQLite opening its connection.
// Callers still use a trusted private directory; this is not filesystem isolation.
func openExistingControl(ctx context.Context, path string, unavailable error) (*Store, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, unavailable
	}
	abs, err := filepath.Abs(path)
	if err != nil || path == "" {
		return nil, unavailable
	}
	info, err := os.Stat(abs)
	if err != nil || !info.Mode().IsRegular() {
		return nil, unavailable
	}
	u := url.URL{Scheme: "file", Path: abs, RawQuery: "mode=rw"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, unavailable
	}
	db.SetMaxOpenConns(1)
	ok := false
	defer func() {
		if !ok {
			db.Close()
		}
	}()
	for _, q := range []string{"PRAGMA busy_timeout=5000", "PRAGMA synchronous=FULL", "PRAGMA foreign_keys=ON"} {
		if _, err = db.ExecContext(ctx, q); err != nil {
			return nil, unavailable
		}
	}
	var version int
	var mode, check string
	if db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version) != nil || (version < 26 || version > 27) || db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode) != nil || mode != "wal" || db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&check) != nil || check != "ok" {
		return nil, unavailable
	}
	ok = true
	return &Store{db: db}, nil
}
