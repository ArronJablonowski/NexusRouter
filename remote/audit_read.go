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
	for _, path := range []string{directory, filepath.Join(directory, "remote.sqlite")} {
		st, err := os.Lstat(path)
		if err != nil {
			return fail(err)
		}
		if st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&0077 != 0 || (path == directory && !st.IsDir()) || (path != directory && !st.Mode().IsRegular()) {
			return fail(ErrDenied)
		}
	}
	u := url.URL{Scheme: "file", Path: filepath.Join(directory, "remote.sqlite")}
	db, err := sql.Open("sqlite", u.String()+"?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return fail(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fail(err)
	}
	defer tx.Rollback()
	var count, version int
	var owner string
	if tx.QueryRowContext(ctx, "SELECT count(*) FROM identity").Scan(&count) != nil || count != 1 || tx.QueryRowContext(ctx, "SELECT version,CASE WHEN length(CAST(instance AS BLOB))<=256 THEN instance END FROM identity").Scan(&version, &owner) != nil || version != Version || owner != instance {
		return fail(ErrConflict)
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
	// Bound each field before allocation, including on a damaged local database.
	rows, err := tx.QueryContext(ctx, `SELECT sequence,
 CASE WHEN length(CAST(at AS BLOB))<=64 THEN at END,
 CASE WHEN length(CAST(caller AS BLOB))<=256 THEN caller END,
 CASE WHEN length(CAST(destination AS BLOB))<=256 THEN destination END,
 CASE WHEN length(CAST(action AS BLOB))<=256 THEN action END,
 CASE WHEN length(CAST(request_id AS BLOB))<=256 THEN request_id END,
 CASE WHEN length(CAST(outcome AS BLOB))<=256 THEN outcome END
 FROM audit WHERE sequence>? AND sequence<=? ORDER BY sequence LIMIT 101`, after, through)
	if err != nil {
		return fail(err)
	}
	defer rows.Close()
	for rows.Next() {
		var entry AuditEntry
		var at string
		if err = rows.Scan(&entry.Sequence, &at, &entry.Caller, &entry.Destination, &entry.Action, &entry.RequestID, &entry.Outcome); err != nil {
			return fail(err)
		}
		entry.At, err = time.Parse(time.RFC3339Nano, at)
		if err != nil || entry.Destination != instance {
			return fail(ErrConflict)
		}
		if len(page.Entries) == 100 {
			page.Next = page.Entries[99].Sequence
			break
		}
		page.Entries = append(page.Entries, entry)
	}
	if err = rows.Err(); err != nil {
		return fail(err)
	}
	if err = rows.Close(); err != nil {
		return fail(err)
	}
	if err = tx.Commit(); err != nil {
		return fail(err)
	}
	return page, nil
}
