package telemetry

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

// ValidateIdentity checks a previously validated, still-open store without
// rescanning its history. It does not replace full validation on a fresh Open.
// A replaced file, schema change, closed handle, or cancelled context fails
// closed; the caller must not fall back to silently opening another database.
func (s *Store) ValidateIdentity(ctx context.Context, path string) error {
	if ctx == nil {
		return errors.New("database context required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	abs, err := filepath.Abs(path)
	if err != nil || s == nil || s.fileInfo == nil || abs != s.path {
		return errors.New("database identity changed")
	}
	check := func() error {
		info, err := os.Stat(abs)
		if err != nil {
			return err
		}
		if !os.SameFile(info, s.fileInfo) {
			return errors.New("database identity changed")
		}
		return nil
	}
	if err := check(); err != nil {
		return err
	}
	var schema, version int
	var journal string
	err = s.db.QueryRowContext(ctx, "SELECT schema_version, user_version, journal_mode FROM pragma_schema_version, pragma_user_version, pragma_journal_mode").Scan(&schema, &version, &journal)
	if err != nil {
		return err
	}
	if schema != s.schemaVersion || version != currentStorageSchema || journal != "wal" {
		return errors.New("database schema changed")
	}
	return check()
}
