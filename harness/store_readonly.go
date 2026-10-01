package harness

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// OpenEvidenceStoreReadOnly opens existing host-owned evidence without creating
// a database or changing journal mode. It cannot be used to append evidence.
// The caller must preserve the ledger's WAL alongside its database.
func OpenEvidenceStoreReadOnly(directory string) (*EvidenceStore, error) {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return nil, ErrInvalid
	}
	st, err := os.Lstat(directory)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&0077 != 0 {
		return nil, ErrInvalid
	}
	path := filepath.Join(directory, "harness-evidence.sqlite")
	st, err = os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
		return nil, ErrInvalid
	}
	u := url.URL{Scheme: "file", Path: path}
	db, err := sql.Open("sqlite", u.String()+"?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var count, version int
	var kind string
	if db.QueryRowContext(ctx, "SELECT count(*) FROM ledger_identity").Scan(&count) != nil || count != 1 || db.QueryRowContext(ctx, "SELECT version,kind FROM ledger_identity WHERE singleton=1").Scan(&version, &kind) != nil || version != Version || kind != "nexus-harness-evidence" {
		db.Close()
		return nil, ErrInvalid
	}
	return &EvidenceStore{db}, nil
}
