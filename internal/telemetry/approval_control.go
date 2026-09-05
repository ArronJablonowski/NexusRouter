package telemetry

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"

	"github.com/ArronJablonowski/DarwinRouter/approvals"
)

// OpenApprovalControl opens only an existing current-schema WAL database.
// It cannot create a missing database or migrate an older one.
func OpenApprovalControl(ctx context.Context, path string) (*Store, error) {
	abs, err := filepath.Abs(path)
	if err != nil || path == "" {
		return nil, approvals.ErrUnavailable
	}
	info, err := os.Stat(abs)
	if err != nil || !info.Mode().IsRegular() {
		return nil, approvals.ErrUnavailable
	}
	u := url.URL{Scheme: "file", Path: abs, RawQuery: "mode=rw"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, approvals.ErrUnavailable
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
			return nil, approvals.ErrUnavailable
		}
	}
	var version int
	var mode, check string
	if db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version) != nil || version != 15 || db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode) != nil || mode != "wal" || db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&check) != nil || check != "ok" {
		return nil, approvals.ErrUnavailable
	}
	ok = true
	return &Store{db: db}, nil
}
