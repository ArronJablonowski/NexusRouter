package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
)

// OpenReadOnly opens an existing database without migrations or creation.
// SQLite's mode=ro also rejects writes through other Store methods.
func OpenReadOnly(ctx context.Context, path string) (*Store, error) {
	abs, err := filepath.Abs(path)
	if err != nil || path == "" {
		return nil, errors.New("invalid database path")
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("database must be a regular file")
	}
	u := url.URL{Scheme: "file", Path: abs, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	failed := true
	defer func() {
		if failed {
			db.Close()
		}
	}()
	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout=5000"); err != nil {
		return nil, err
	}
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return nil, err
	}
	if version < 1 || version > 14 {
		return nil, errors.New("unsupported database version")
	}
	var check string
	if err := db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&check); err != nil {
		return nil, err
	}
	if check != "ok" {
		return nil, errors.New("database integrity check failed")
	}
	failed = false
	return s, nil
}
