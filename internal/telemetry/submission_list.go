package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/submissions"
)

// ListSubmissions reads a bounded insertion-fenced page. State membership remains
// live between calls; no request, result, token, or event body is returned.
func (s *Store) ListSubmissions(ctx context.Context, opts submissions.ListOptions) (submissions.Page, error) {
	page := submissions.Page{Version: 1, Items: []submissions.Summary{}}
	if err := opts.Validate(); err != nil {
		return page, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	var version int
	if err = tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return page, err
	}
	if version < 12 {
		return page, tx.Commit()
	}
	cursor := submissions.ListCursor{Version: 1, State: opts.State}
	if opts.After != "" {
		cursor, _ = submissions.DecodeListCursor(opts.After)
	} else if err = tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(rowid),0) FROM submissions").Scan(&cursor.HighWater); err != nil {
		return page, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT rowid FROM submissions WHERE rowid>? AND rowid<=? AND (?='' OR state=?) ORDER BY rowid LIMIT ?`, cursor.Last, cursor.HighWater, opts.State, opts.State, opts.Limit+1)
	if err != nil {
		return page, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return page, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return page, err
	}
	now := time.Now()
	for i, id := range ids {
		if i == opts.Limit {
			page.HasMore = true
			break
		}
		item, e := readSubmissionSummary(ctx, tx, id, now)
		if e != nil {
			return page, e
		}
		candidate := page
		candidate.Items = append(append([]submissions.Summary{}, page.Items...), item)
		candidate.HasMore = i+1 < len(ids)
		if candidate.HasMore {
			next := cursor
			next.Last = id
			candidate.NextCursor, _ = submissions.EncodeListCursor(next)
		} else {
			candidate.NextCursor = ""
		}
		body, e := json.Marshal(candidate)
		if e != nil {
			return page, submissions.ErrInvalid
		}
		if len(body) > submissions.MaxPageBytes {
			if len(page.Items) == 0 {
				return page, submissions.ErrInvalid
			}
			page.HasMore = true
			break
		}
		page = candidate
		cursor.Last = id
	}
	if page.HasMore {
		page.NextCursor, _ = submissions.EncodeListCursor(cursor)
	} else {
		page.NextCursor = ""
	}
	if page.Validate() != nil {
		return submissions.Page{}, submissions.ErrInvalid
	}
	return page, tx.Commit()
}

func readSubmissionSummary(ctx context.Context, tx *sql.Tx, rowid int64, now time.Time) (submissions.Summary, error) {
	item := submissions.Summary{Version: 1, TaskIDs: []string{}}
	var created, updated, lease string
	var canceled int
	// The predicates execute before values are copied into Go. Corrupt oversized
	// strings are rejected without allocating their contents in the caller.
	err := tx.QueryRowContext(ctx, `SELECT id,state,created_at,updated_at,config_digest,cancel_requested,lease_expires_at,error_code FROM submissions WHERE rowid=?
 AND length(CAST(id AS BLOB))<=128 AND length(CAST(state AS BLOB))<=16
 AND length(CAST(created_at AS BLOB))<=64 AND length(CAST(updated_at AS BLOB))<=64
 AND length(CAST(config_digest AS BLOB))=64 AND length(CAST(lease_expires_at AS BLOB))<=64
 AND length(CAST(error_code AS BLOB))<=64`, rowid).Scan(&item.ID, &item.State, &created, &updated, &item.ConfigDigest, &canceled, &lease, &item.ErrorCode)
	if err != nil {
		if err == sql.ErrNoRows {
			return item, submissions.ErrInvalid
		}
		return item, err
	}
	if canceled != 0 && canceled != 1 {
		return item, submissions.ErrInvalid
	}
	item.CancelRequested = canceled == 1
	item.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return item, submissions.ErrInvalid
	}
	item.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	if err != nil {
		return item, submissions.ErrInvalid
	}
	if lease != "" {
		expiry, e := time.Parse(time.RFC3339Nano, lease)
		if e != nil {
			return item, submissions.ErrInvalid
		}
		item.LeaseExpiresAt = &expiry
		item.LeaseExpired = item.State == "running" && !now.Before(expiry)
	}
	rows, err := tx.QueryContext(ctx, `SELECT CASE WHEN length(CAST(task_id AS BLOB))<=128 THEN task_id ELSE NULL END FROM events WHERE json_extract(body,'$.kind')='task.started' AND json_extract(body,'$.data.submission_id')=? ORDER BY rowid LIMIT 1001`, item.ID)
	if err != nil {
		return item, err
	}
	defer rows.Close()
	for rows.Next() {
		var id sql.NullString
		if err = rows.Scan(&id); err != nil {
			return item, err
		}
		if !id.Valid {
			return item, submissions.ErrInvalid
		}
		item.TaskIDs = append(item.TaskIDs, id.String)
		if len(item.TaskIDs) > 1000 {
			return item, submissions.ErrInvalid
		}
	}
	if err = rows.Err(); err != nil {
		return item, err
	}
	if item.Validate() != nil {
		return item, submissions.ErrInvalid
	}
	return item, nil
}
