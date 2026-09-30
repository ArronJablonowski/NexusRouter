package telemetry

import (
	"context"
	"database/sql"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ArronJablonowski/NexusRouter/approvals"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

// observeScopeLeases projects counts only. Expired unreleased holders remain
// blockers; observing them neither establishes their death nor releases them.
func observeScopeLeases(ctx context.Context, tx *sql.Tx, scope string, now time.Time) (*approvals.ScopeLeaseObservation, error) {
	rows, err := tx.QueryContext(ctx, `SELECT
	CASE WHEN typeof(token)='text' AND length(CAST(token AS BLOB)) BETWEEN 1 AND 512 THEN token END,
	CASE WHEN typeof(task_id)='text' AND length(CAST(task_id AS BLOB)) BETWEEN 1 AND 128 THEN task_id END,
	CASE WHEN typeof(owner)='text' AND length(CAST(owner AS BLOB)) BETWEEN 1 AND 512 THEN owner END,
	CASE WHEN typeof(scope)='text' AND length(CAST(scope AS BLOB)) BETWEEN 1 AND 512 THEN scope END,
	CASE WHEN typeof(expires)='integer' THEN expires END,
	CASE WHEN typeof(writer)='integer' THEN writer END
	FROM resource_leases WHERE
	(scope=? OR (?=1 AND (scope='workspace' OR scope GLOB 'create_*')))
	AND released=0 LIMIT 1001`, scope, filesystemLeaseScope(scope))
	if err != nil {
		return nil, approvals.ErrUnavailable
	}
	defer rows.Close()
	out := &approvals.ScopeLeaseObservation{Version: 1, OverlapPolicyVersion: 1}
	writers := make(map[string]bool)
	count := 0
	for rows.Next() {
		count++
		if count > 1000 {
			return nil, approvals.ErrInvalid
		}
		var token, task, owner, storedScope sql.NullString
		var expires, writer sql.NullInt64
		if rows.Scan(&token, &task, &owner, &storedScope, &expires, &writer) != nil {
			return nil, approvals.ErrUnavailable
		}
		if !leaseObservationIdentity(token) || !leaseObservationIdentity(owner) || !leaseObservationIdentity(storedScope) || !task.Valid || !sessions.ValidEventPageID(task.String) || !expires.Valid || expires.Int64 < 0 || time.Unix(0, expires.Int64).UTC().Year() >= 2261 || !writer.Valid || (writer.Int64 != 0 && writer.Int64 != 1) || !(storedScope.String == scope || filesystemLeaseScope(scope) && filesystemLeaseScope(storedScope.String)) {
			return nil, approvals.ErrInvalid
		}
		live := expires.Int64 > now.UnixNano()
		if writer.Int64 == 1 {
			if writers[storedScope.String] {
				return nil, approvals.ErrInvalid
			}
			writers[storedScope.String] = true
			if live {
				out.LiveWriters++
			} else {
				out.ExpiredWriters++
			}
		} else if live {
			out.LiveReaders++
		} else {
			out.ExpiredReaders++
		}
	}
	if rows.Err() != nil || rows.Close() != nil {
		return nil, approvals.ErrUnavailable
	}
	return out, nil
}

func leaseObservationIdentity(v sql.NullString) bool {
	return v.Valid && utf8.ValidString(v.String) && strings.TrimSpace(v.String) == v.String && strings.IndexFunc(v.String, unicode.IsControl) < 0
}
