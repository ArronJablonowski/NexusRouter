package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/sessions"
	"github.com/ArronJablonowski/DarwinRouter/workers"
)

// TaskLeaseStatus reads one coherent bounded snapshot without probing ownership,
// creating guards, migrating storage, releasing resources, or executing work.
func (s *Store) TaskLeaseStatus(ctx context.Context, task string, now time.Time) (workers.TaskLeaseStatus, error) {
	zero := workers.TaskLeaseStatus{}
	now = now.UTC()
	if ctx == nil || !sessions.ValidEventPageID(task) || now.Year() < 1970 || now.Year() >= 2261 {
		return zero, workers.ErrTaskLeaseStatus
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return zero, workers.ErrTaskLeaseStatus
	}
	defer tx.Rollback()
	var version int
	if tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version) != nil || version < 1 || version > currentStorageSchema {
		return zero, workers.ErrTaskLeaseStatus
	}
	var exists int
	if err = tx.QueryRowContext(ctx, "SELECT 1 FROM task_heads WHERE task_id=?", task).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return zero, sql.ErrNoRows
		}
		return zero, workers.ErrTaskLeaseStatus
	}
	snapshot, err := taskSnapshot(ctx, tx, task)
	if err != nil {
		return zero, workers.ErrTaskLeaseStatus
	}
	out := workers.TaskLeaseStatus{Version: 1, TaskID: task, TaskState: snapshot.State, Sequence: snapshot.Sequence, ObservedAt: now, StorageSchema: version}
	if version >= 3 {
		out.Leases = &workers.TaskLeaseCounts{}
		if version >= 23 {
			out.Recoveries = &workers.TaskRecoveryCounts{}
		}
		if err = observeTaskLeases(ctx, tx, &out); err != nil {
			return zero, workers.ErrTaskLeaseStatus
		}
	}
	if out.Validate() != nil || tx.Commit() != nil {
		return zero, workers.ErrTaskLeaseStatus
	}
	return out, nil
}

func observeTaskLeases(ctx context.Context, tx *sql.Tx, out *workers.TaskLeaseStatus) error {
	rows, err := tx.QueryContext(ctx, `SELECT
	CASE WHEN typeof(token)='text' AND length(CAST(token AS BLOB)) BETWEEN 1 AND 512 THEN token END,
	CASE WHEN typeof(owner)='text' AND length(CAST(owner AS BLOB)) BETWEEN 1 AND 512 THEN owner END,
	CASE WHEN typeof(scope)='text' AND length(CAST(scope AS BLOB)) BETWEEN 1 AND 512 THEN scope END,
	CASE WHEN typeof(expires)='integer' THEN expires END,
	CASE WHEN typeof(writer)='integer' THEN writer END,
	CASE WHEN typeof(released)='integer' THEN released END
	FROM resource_leases WHERE task_id=? LIMIT 1001`, out.TaskID)
	if err != nil {
		return err
	}
	defer rows.Close()
	var tokens []string
	for rows.Next() {
		var token, owner, scope sql.NullString
		var expires, writer, released sql.NullInt64
		if rows.Scan(&token, &owner, &scope, &expires, &writer, &released) != nil || len(tokens) >= 1000 || !leaseObservationIdentity(token) || !leaseObservationIdentity(owner) || !leaseObservationIdentity(scope) || !expires.Valid || expires.Int64 < 0 || time.Unix(0, expires.Int64).UTC().Year() >= 2261 || !writer.Valid || writer.Int64 < 0 || writer.Int64 > 1 || !released.Valid || released.Int64 < 0 || released.Int64 > 1 {
			return workers.ErrTaskLeaseStatus
		}
		tokens = append(tokens, token.String)
		l := out.Leases
		switch {
		case released.Int64 == 1 && writer.Int64 == 1:
			l.ReleasedWriters++
		case released.Int64 == 1:
			l.ReleasedReaders++
		case writer.Int64 == 1 && expires.Int64 > out.ObservedAt.UnixNano():
			l.LiveWriters++
		case writer.Int64 == 1:
			l.ExpiredWriters++
		case expires.Int64 > out.ObservedAt.UnixNano():
			l.LiveReaders++
		default:
			l.ExpiredReaders++
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	if out.StorageSchema < 22 {
		return nil
	}
	for _, token := range tokens {
		c, err := readRecoveryLease(ctx, tx, token)
		if err != nil || c.Task != out.TaskID {
			return workers.ErrTaskLeaseStatus
		}
		if out.StorageSchema < 23 {
			continue
		}
		found, err := existingLeaseRecovery(ctx, tx, c)
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		var body []byte
		var receipt leaseRecoveryReceipt
		if tx.QueryRowContext(ctx, `SELECT CASE WHEN length(CAST(body AS BLOB)) BETWEEN 1 AND 2048 THEN body END FROM lease_recoveries WHERE lease_token=?`, token).Scan(&body) != nil || json.Unmarshal(body, &receipt) != nil {
			return workers.ErrTaskLeaseStatus
		}
		if receipt.Reason == "terminal_reader_owner_unlocked" {
			out.Recoveries.TerminalReaders++
		} else {
			out.Recoveries.OrphanWorkers++
			if receipt.ChildTaskID != "" {
				out.Recoveries.InterruptedChildren++
			}
		}
	}
	return nil
}
