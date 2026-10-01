package remote

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// AuditEntry contains control metadata only. It is not evidence of task quality.
type AuditEntry struct {
	Sequence    int64     `json:"sequence"`
	At          time.Time `json:"at"`
	Caller      string    `json:"caller"`
	Destination string    `json:"destination"`
	Action      string    `json:"action"`
	RequestID   string    `json:"request_id"`
	Outcome     string    `json:"outcome"`
}

type AuditPage struct {
	Instance string       `json:"instance"`
	Through  int64        `json:"through"`
	Entries  []AuditEntry `json:"entries"`
	Next     int64        `json:"next,omitempty"`
}

// ReadAuditPage inspects an existing private journal without creating it or
// acquiring runtime authority. The first page captures a high-water mark;
// subsequent pages must supply it. Later appends are excluded from that scan.
// This is local administrator access, not a peer-visible remote endpoint.
func ReadAuditPage(ctx context.Context, directory, instance string, after, through int64) (AuditPage, error) {
	fail := func(err error) (AuditPage, error) { return AuditPage{}, err }
	if !id(instance) || !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || after < 0 || through < 0 || after > through {
		return fail(ErrInvalid)
	}
	db, err := openAuditDatabase(directory, false)
	if err != nil {
		return fail(err)
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fail(err)
	}
	defer tx.Rollback()
	if err = checkAuditIdentity(ctx, tx, instance); err != nil {
		return fail(err)
	}
	var maximum int64
	if err = tx.QueryRowContext(ctx, "SELECT coalesce(max(sequence),0) FROM audit").Scan(&maximum); err != nil {
		return fail(err)
	}
	if through == 0 {
		through = maximum
	}
	if through > maximum {
		return fail(ErrConflict)
	}
	page := AuditPage{Instance: instance, Through: through, Entries: make([]AuditEntry, 0)}
	entries, err := readAuditRows(ctx, tx, instance, after, through, 101)
	if err != nil {
		return fail(err)
	}
	if len(entries) > 100 {
		page.Next = entries[99].Sequence
		entries = entries[:100]
	}
	page.Entries = entries
	if err = tx.Commit(); err != nil {
		return fail(err)
	}
	return page, nil
}

func openAuditDatabase(directory string, writable bool) (*sql.DB, error) {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return nil, ErrInvalid
	}
	for _, path := range []string{directory, filepath.Join(directory, "remote.sqlite")} {
		st, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&0077 != 0 || (path == directory && !st.IsDir()) || (path != directory && !st.Mode().IsRegular()) {
			return nil, ErrDenied
		}
	}
	u := url.URL{Scheme: "file", Path: filepath.Join(directory, "remote.sqlite")}
	options := "?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(5000)"
	if writable {
		options = "?mode=rw&_pragma=synchronous(FULL)&_pragma=busy_timeout(5000)"
	}
	db, err := sql.Open("sqlite", u.String()+options)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func readAuditRows(ctx context.Context, tx *sql.Tx, instance string, after, through int64, limit int) ([]AuditEntry, error) {
	// Count bytes rather than SQLite text characters (which stop at NUL).
	rows, err := tx.QueryContext(ctx, `SELECT sequence,
 CASE WHEN length(CAST(at AS BLOB))<=64 THEN at END,
 CASE WHEN length(CAST(caller AS BLOB))<=256 THEN caller END,
 CASE WHEN length(CAST(destination AS BLOB))<=256 THEN destination END,
 CASE WHEN length(CAST(action AS BLOB))<=256 THEN action END,
 CASE WHEN length(CAST(request_id AS BLOB))<=256 THEN request_id END,
 CASE WHEN length(CAST(outcome AS BLOB))<=256 THEN outcome END
 FROM audit WHERE sequence>? AND sequence<=? ORDER BY sequence LIMIT ?`, after, through, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := make([]AuditEntry, 0)
	for rows.Next() {
		var entry AuditEntry
		var at string
		if err = rows.Scan(&entry.Sequence, &at, &entry.Caller, &entry.Destination, &entry.Action, &entry.RequestID, &entry.Outcome); err != nil {
			return nil, err
		}
		entry.At, err = time.Parse(time.RFC3339Nano, at)
		if err != nil || entry.Destination != instance {
			return nil, ErrConflict
		}
		entries = append(entries, entry)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}
