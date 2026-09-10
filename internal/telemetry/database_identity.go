package telemetry

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

// SameDatabaseFile reports whether configuredPath names the same regular file
// as the main database behind this live Store. It compares filesystem identity,
// not path spelling or the portable workspace identity stored in the database.
func (s *Store) SameDatabaseFile(ctx context.Context, configuredPath string) (bool, error) {
	if s == nil || s.db == nil || ctx == nil || ctx.Err() != nil || configuredPath == "" {
		return false, errors.New("database identity unavailable")
	}
	rows, err := s.db.QueryContext(ctx, "PRAGMA database_list")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	mainPath := ""
	for rows.Next() {
		var sequence int
		var name, path string
		if err = rows.Scan(&sequence, &name, &path); err != nil {
			return false, err
		}
		if name == "main" {
			mainPath = path
		}
	}
	if err = rows.Err(); err != nil {
		return false, err
	}
	if mainPath == "" {
		return false, errors.New("main database identity unavailable")
	}
	mainPath, err = filepath.EvalSymlinks(mainPath)
	if err != nil {
		return false, err
	}
	configuredPath, err = filepath.EvalSymlinks(configuredPath)
	if err != nil {
		return false, err
	}
	mainInfo, err := os.Stat(mainPath)
	if err != nil {
		return false, err
	}
	configuredInfo, err := os.Stat(configuredPath)
	if err != nil {
		return false, err
	}
	if !mainInfo.Mode().IsRegular() || !configuredInfo.Mode().IsRegular() {
		return false, errors.New("database identity is not a regular file")
	}
	if ctx.Err() != nil {
		return false, errors.New("database identity unavailable")
	}
	return os.SameFile(mainInfo, configuredInfo), nil
}
