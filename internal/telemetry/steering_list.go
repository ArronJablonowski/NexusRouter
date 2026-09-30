package telemetry

import (
	"context"
	"database/sql"

	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

// ListSteering returns the complete lifetime queue in insertion order. Payload
// loading is bounded by 32 messages of at most 64 KiB (2 MiB total text).
// A read transaction keeps the queue, applied counters and task head consistent.
func (s *Store) ListSteering(ctx context.Context, task string) ([]runtime.SteeringMessage, error) {
	if !sessions.ValidEventPageID(task) {
		return nil, sessions.ErrHistory
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return nil, err
	}
	var state sql.NullString
	var head int64
	if err := tx.QueryRowContext(ctx, `SELECT CASE WHEN length(CAST(state AS BLOB))<=16 THEN state END,sequence FROM task_heads WHERE task_id=?`, task).Scan(&state, &head); err != nil {
		return nil, err
	}
	if !state.Valid || head < 1 || (state.String != "running" && state.String != "completed" && state.String != "failed" && state.String != "canceled") {
		return nil, sessions.ErrHistory
	}
	out := []runtime.SteeringMessage{}
	if version < 14 {
		return out, tx.Commit()
	}
	count, _, err := steeringCounts(ctx, tx, task)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT CASE WHEN length(CAST(id AS BLOB))<=128 THEN id END FROM task_steering WHERE task_id=? ORDER BY rowid LIMIT 33`, task)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id sql.NullString
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		if !id.Valid || len(ids) >= 32 {
			rows.Close()
			return nil, sessions.ErrHistory
		}
		ids = append(ids, id.String)
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if len(ids) != count {
		return nil, sessions.ErrHistory
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			return nil, sessions.ErrHistory
		}
		seen[id] = true
		m, err := readSteering(ctx, tx, task, id)
		if err != nil {
			return nil, sessions.ErrHistory
		}
		if m.AppliedSequence != nil && *m.AppliedSequence > head {
			return nil, sessions.ErrHistory
		}
		out = append(out, m)
	}
	return out, tx.Commit()
}
