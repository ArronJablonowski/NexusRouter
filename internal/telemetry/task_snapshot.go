package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

// TaskSnapshot reconstructs one coherent journal snapshot without executing or
// repairing it. All payload lengths are checked before any payload is loaded.
func (s *Store) TaskSnapshot(ctx context.Context, task string) (sessions.Snapshot, error) {
	zero := sessions.Snapshot{}
	if !sessions.ValidEventPageID(task) {
		return zero, sessions.ErrHistory
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	var session, state sql.NullString
	var head int64
	err = tx.QueryRowContext(ctx, `SELECT CASE WHEN length(CAST(session_id AS BLOB))<=128 THEN session_id END,CASE WHEN length(CAST(state AS BLOB))<=16 THEN state END,sequence FROM task_heads WHERE task_id=?`, task).Scan(&session, &state, &head)
	if err != nil {
		return zero, err
	}
	if !session.Valid || !state.Valid || !sessions.ValidEventPageID(session.String) || head < 1 || head > 10000 {
		return zero, sessions.ErrHistory
	}
	switch state.String {
	case "running", "completed", "failed", "canceled":
	default:
		return zero, sessions.ErrHistory
	}
	rows, err := tx.QueryContext(ctx, `SELECT CASE WHEN length(CAST(id AS BLOB))<=128 THEN id END,sequence,length(CAST(body AS BLOB)) FROM events WHERE task_id=? ORDER BY sequence LIMIT 10001`, task)
	if err != nil {
		return zero, err
	}
	type entry struct {
		id             string
		sequence, size int64
	}
	entries := []entry{}
	var total int64
	for rows.Next() {
		var id sql.NullString
		var sequence, size int64
		if err = rows.Scan(&id, &sequence, &size); err != nil {
			rows.Close()
			return zero, err
		}
		if !id.Valid || id.String == "" || sequence != int64(len(entries)+1) || len(entries) >= 10000 || size < 1 || size > (8<<20)-total {
			rows.Close()
			return zero, sessions.ErrHistory
		}
		total += size
		entries = append(entries, entry{id.String, sequence, size})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return zero, err
	}
	if int64(len(entries)) != head {
		return zero, sessions.ErrHistory
	}
	events := make(snapshotEvents, 0, len(entries))
	for _, entry := range entries {
		var body []byte
		if err = tx.QueryRowContext(ctx, `SELECT body FROM events WHERE task_id=? AND sequence=?`, task, entry.sequence).Scan(&body); err != nil {
			return zero, err
		}
		var event runtime.Event
		if int64(len(body)) != entry.size || json.Unmarshal(body, &event) != nil || event.Validate() != nil || event.ID != entry.id || event.TaskID != task || event.SessionID != session.String || event.Sequence != entry.sequence {
			return zero, sessions.ErrHistory
		}
		events = append(events, event)
	}
	if !sessions.EventPageStateMatches(state.String, events[len(events)-1].Kind) {
		return zero, sessions.ErrHistory
	}
	snapshot, err := sessions.Replay(ctx, events, task)
	if err != nil {
		return zero, err
	}
	if snapshot.Sequence != head || snapshot.State != state.String || snapshot.SessionID != session.String {
		return zero, sessions.ErrHistory
	}
	if err = tx.Commit(); err != nil {
		return zero, err
	}
	return snapshot, nil
}

type snapshotEvents []runtime.Event

func (events snapshotEvents) Read(ctx context.Context, task string, after int64, limit int) ([]runtime.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if after < 0 || after > int64(len(events)) || limit < 1 {
		return nil, sessions.ErrHistory
	}
	end := min(int64(len(events)), after+int64(limit))
	return events[after:end], nil
}
