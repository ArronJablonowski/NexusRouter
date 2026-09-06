package telemetry

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/sessions"
	"github.com/ArronJablonowski/DarwinRouter/workers"
)

// ObserveLeaseAttentionPage records overdue ownership without probing, releasing
// or executing anything. The private rowid sweep is separate from public IDs.
// A page commits atomically; an invalid page returns its original cursor.
func (s *Store) ObserveLeaseAttentionPage(ctx context.Context, after string, now time.Time, limit int) (string, int, error) {
	return s.observeLeaseAttentionPage(ctx, after, now, limit, 0)
}

// through bounds an internal sweep to exactly one selected row. A vanished or
// newly ineligible candidate must not make the query fall through to a later row.
func (s *Store) observeLeaseAttentionPage(ctx context.Context, after string, now time.Time, limit int, through int64) (string, int, error) {
	bad := func() (string, int, error) { return after, 0, workers.ErrLeaseAttention }
	now = now.UTC()
	var cursor int64
	if after != "" {
		var err error
		cursor, err = strconv.ParseInt(after, 10, 64)
		if err != nil || cursor < 1 || strconv.FormatInt(cursor, 10) != after {
			return bad()
		}
	}
	if ctx == nil || limit < 1 || limit > 100 || now.Year() < 1970 || now.Year() >= 2261 || through < 0 || through > 0 && through <= cursor {
		return bad()
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return bad()
	}
	defer tx.Rollback()
	if err = reserveLeaseAttention(ctx, tx); err != nil {
		return bad()
	}
	query := `SELECT l.rowid,
CASE WHEN typeof(l.token)='text' AND length(CAST(l.token AS BLOB)) BETWEEN 1 AND 512 THEN l.token END,
CASE WHEN typeof(l.task_id)='text' AND length(CAST(l.task_id AS BLOB)) BETWEEN 1 AND 128 THEN l.task_id END,
CASE WHEN typeof(l.expires)='integer' THEN l.expires END,
CASE WHEN typeof(l.writer)='integer' THEN l.writer END,
CASE WHEN typeof(l.released)='integer' THEN l.released END,
EXISTS(SELECT 1 FROM task_heads h WHERE h.task_id=l.task_id)
FROM resource_leases l WHERE l.rowid>? AND ((l.released=0 AND l.expires<=?) OR EXISTS(SELECT 1 FROM lease_attention a WHERE a.lease_token=l.token))`
	args := []any{cursor, now.UnixNano()}
	if through > 0 {
		query += " AND l.rowid<=?"
		args = append(args, through)
	}
	query += " ORDER BY l.rowid LIMIT ?"
	args = append(args, limit)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return bad()
	}
	type candidate struct {
		row              int64
		token, task      string
		expires          time.Time
		writer, released bool
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		var token, task sql.NullString
		var expiry, writer, released sql.NullInt64
		var bound bool
		if rows.Scan(&c.row, &token, &task, &expiry, &writer, &released, &bound) != nil || c.row <= cursor || !leaseObservationIdentity(token) || !task.Valid || !sessions.ValidEventPageID(task.String) || !expiry.Valid || expiry.Int64 < 0 || time.Unix(0, expiry.Int64).UTC().Year() >= 2261 || !writer.Valid || writer.Int64 < 0 || writer.Int64 > 1 || !released.Valid || released.Int64 < 0 || released.Int64 > 1 || !bound {
			rows.Close()
			return bad()
		}
		c.token, c.task, c.expires, c.writer, c.released = token.String, task.String, time.Unix(0, expiry.Int64).UTC(), writer.Int64 == 1, released.Int64 == 1
		candidates = append(candidates, c)
	}
	if rows.Err() != nil || rows.Close() != nil {
		return bad()
	}
	changed := 0
	next := ""
	for _, c := range candidates {
		record := workers.LeaseAttention{Version: 1, ID: rand.Text(), TaskID: c.task, Writer: c.writer, FirstObserved: now, UpdatedAt: now, LeaseExpires: c.expires, State: "open", Reason: "expired_unreleased"}
		if c.released {
			record.State, record.Reason = "resolved", "lease_released"
		} else if c.expires.After(now) {
			record.State, record.Reason = "resolved", "lease_renewed"
		}
		var id, task, state sql.NullString
		var body []byte
		var historySequence int64
		err := tx.QueryRowContext(ctx, `SELECT CASE WHEN length(CAST(id AS BLOB)) BETWEEN 1 AND 128 THEN id END,CASE WHEN length(CAST(task_id AS BLOB)) BETWEEN 1 AND 128 THEN task_id END,CASE WHEN length(CAST(state AS BLOB)) BETWEEN 1 AND 16 THEN state END,CASE WHEN length(CAST(body AS BLOB)) BETWEEN 1 AND 4096 THEN body END FROM lease_attention WHERE lease_token=?`, c.token).Scan(&id, &task, &state, &body)
		exists := err == nil
		if exists {
			previous, decodeErr := decodeLeaseAttention(id.String, task.String, state.String, body)
			if !id.Valid || !task.Valid || !state.Valid || decodeErr != nil || previous.TaskID != c.task || previous.Writer != c.writer || now.Before(previous.UpdatedAt) {
				return bad()
			}
			record.ID, record.FirstObserved = previous.ID, previous.FirstObserved
			historySequence, err = attentionHistoryHead(ctx, tx, previous)
			if err != nil {
				return bad()
			}
			if previous.State == record.State && previous.Reason == record.Reason && previous.LeaseExpires.Equal(record.LeaseExpires) {
				continue
			}
		} else if !errors.Is(err, sql.ErrNoRows) || record.State != "open" {
			return bad()
		}
		if record.Validate() != nil {
			return bad()
		}
		encoded, err := json.Marshal(record)
		if err != nil || len(encoded) > 4096 {
			return bad()
		}
		var result sql.Result
		if exists {
			result, err = tx.ExecContext(ctx, `UPDATE lease_attention SET state=?,body=? WHERE lease_token=? AND id=?`, record.State, encoded, c.token, record.ID)
		} else {
			result, err = tx.ExecContext(ctx, `INSERT INTO lease_attention(id,lease_token,task_id,state,body) VALUES(?,?,?,?,?)`, record.ID, c.token, record.TaskID, record.State, encoded)
		}
		if err != nil {
			return bad()
		}
		if n, err := result.RowsAffected(); err != nil || n != 1 {
			return bad()
		}
		if appendAttentionTransition(ctx, tx, record, historySequence) != nil {
			return bad()
		}
		changed++
	}
	if len(candidates) == limit {
		next = strconv.FormatInt(candidates[len(candidates)-1].row, 10)
	}
	if tx.Commit() != nil {
		return bad()
	}
	return next, changed, nil
}

// Reserve the writer before the first snapshot read. Reading user_version first
// permits a concurrent WAL commit to make this transaction's later write upgrade
// fail with SQLITE_BUSY_SNAPSHOT; a busy timeout cannot repair that old snapshot.
// WHERE 0 changes no rows, and unsupported/missing tables still fail closed.
func reserveLeaseAttention(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `UPDATE lease_attention SET state=state WHERE 0`); err != nil {
		return err
	}
	var schema int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&schema); err != nil {
		return err
	}
	if schema < 26 || schema > 27 {
		return workers.ErrLeaseAttention
	}
	return nil
}
