package telemetry

import (
	"context"
	"database/sql"
	"sort"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/sessions"
	"github.com/ArronJablonowski/DarwinRouter/workers"
)

// ScopeLeaseStatus observes bounded unresolved holders without probing ownership,
// migrating storage, or modifying lease state. Expiry does not imply release.
func (s *Store) ScopeLeaseStatus(ctx context.Context, scope string, now time.Time) (workers.ScopeLeaseStatus, error) {
	zero := workers.ScopeLeaseStatus{}
	now = now.UTC()
	if ctx == nil || !workers.ValidLeaseScope(scope) || now.Year() < 1970 || now.Year() >= 2261 {
		return zero, workers.ErrScopeLeaseStatus
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return zero, workers.ErrScopeLeaseStatus
	}
	defer tx.Rollback()
	var version int
	if tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version) != nil || version < 1 || version > 24 {
		return zero, workers.ErrScopeLeaseStatus
	}
	out := workers.ScopeLeaseStatus{Version: 1, OverlapPolicyVersion: 1, Scope: scope, ObservedAt: now, StorageSchema: version, Available: version >= 3, Holders: []workers.ScopeLeaseHolder{}}
	if out.Available {
		if err = observeScopeHolders(ctx, tx, &out); err != nil {
			return zero, workers.ErrScopeLeaseStatus
		}
	}
	if out.Validate() != nil || tx.Commit() != nil {
		return zero, workers.ErrScopeLeaseStatus
	}
	return out, nil
}

func observeScopeHolders(ctx context.Context, tx *sql.Tx, out *workers.ScopeLeaseStatus) error {
	rows, err := tx.QueryContext(ctx, `SELECT
CASE WHEN typeof(token)='text' AND length(CAST(token AS BLOB)) BETWEEN 1 AND 512 THEN token END,
CASE WHEN typeof(task_id)='text' AND length(CAST(task_id AS BLOB)) BETWEEN 1 AND 128 THEN task_id END,
CASE WHEN typeof(owner)='text' AND length(CAST(owner AS BLOB)) BETWEEN 1 AND 512 THEN owner END,
CASE WHEN typeof(scope)='text' AND length(CAST(scope AS BLOB)) BETWEEN 1 AND 512 THEN scope END,
CASE WHEN typeof(expires)='integer' THEN expires END,
CASE WHEN typeof(writer)='integer' THEN writer END,
CASE WHEN typeof(released)='integer' THEN released END
FROM resource_leases WHERE (scope=? OR (?=1 AND (scope='workspace' OR scope GLOB 'create_*')))
AND released=0 LIMIT 1001`, out.Scope, filesystemLeaseScope(out.Scope))
	if err != nil {
		return err
	}
	defer rows.Close()
	holders := make(map[string]*workers.ScopeLeaseHolder)
	writers := make(map[string]bool)
	count := 0
	for rows.Next() {
		count++
		var token, task, owner, scope sql.NullString
		var expires, writer, released sql.NullInt64
		if count > 1000 || rows.Scan(&token, &task, &owner, &scope, &expires, &writer, &released) != nil || !leaseObservationIdentity(token) || !leaseObservationIdentity(owner) || !scope.Valid || !workers.ValidLeaseScope(scope.String) || !task.Valid || !sessions.ValidEventPageID(task.String) || !expires.Valid || expires.Int64 < 0 || time.Unix(0, expires.Int64).UTC().Year() >= 2261 || !writer.Valid || writer.Int64 < 0 || writer.Int64 > 1 || !released.Valid || released.Int64 != 0 || !(scope.String == out.Scope || filesystemLeaseScope(out.Scope) && filesystemLeaseScope(scope.String)) {
			return workers.ErrScopeLeaseStatus
		}
		h := holders[task.String]
		if h == nil {
			h = &workers.ScopeLeaseHolder{TaskID: task.String}
			holders[task.String] = h
		}
		live := expires.Int64 > out.ObservedAt.UnixNano()
		if writer.Int64 == 1 {
			if writers[scope.String] {
				return workers.ErrScopeLeaseStatus
			}
			writers[scope.String] = true
			if live {
				h.LiveWriters++
			} else {
				h.ExpiredWriters++
			}
		} else if live {
			h.LiveReaders++
		} else {
			h.ExpiredReaders++
		}
	}
	if rows.Err() != nil || rows.Close() != nil {
		return workers.ErrScopeLeaseStatus
	}
	for task, holder := range holders {
		var session, state sql.NullString
		var sequence sql.NullInt64
		if tx.QueryRowContext(ctx, `SELECT
CASE WHEN typeof(session_id)='text' AND length(CAST(session_id AS BLOB)) BETWEEN 1 AND 128 THEN session_id END,
CASE WHEN typeof(state)='text' AND length(CAST(state AS BLOB)) BETWEEN 1 AND 16 THEN state END,
CASE WHEN typeof(sequence)='integer' THEN sequence END
FROM task_heads WHERE task_id=?`, task).Scan(&session, &state, &sequence) != nil || !session.Valid || !sessions.ValidEventPageID(session.String) || !state.Valid || !sequence.Valid || sequence.Int64 < 1 {
			return workers.ErrScopeLeaseStatus
		}
		switch state.String {
		case "running":
		case "completed", "failed", "canceled":
			if sequence.Int64 < 2 {
				return workers.ErrScopeLeaseStatus
			}
		default:
			return workers.ErrScopeLeaseStatus
		}
		out.Holders = append(out.Holders, *holder)
	}
	sort.Slice(out.Holders, func(i, j int) bool { return out.Holders[i].TaskID < out.Holders[j].TaskID })
	return nil
}
