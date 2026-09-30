package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
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
	snapshot, err := taskSnapshot(ctx, tx, task)
	if err != nil {
		return zero, err
	}
	if err = tx.Commit(); err != nil {
		return zero, err
	}
	return snapshot, nil
}

// taskSnapshot also serves authorization checks which already hold the writer
// transaction. Validation and consumption then observe the same journal state.
func taskSnapshot(ctx context.Context, tx *sql.Tx, task string) (sessions.Snapshot, error) {
	return taskSnapshotWithEvents(ctx, tx, task, nil)
}

// The optional event capture is populated only after complete replay validation.
func taskSnapshotWithEvents(ctx context.Context, tx *sql.Tx, task string, captured *[]runtime.Event) (sessions.Snapshot, error) {
	zero := sessions.Snapshot{}
	var session, state sql.NullString
	var head int64
	err := tx.QueryRowContext(ctx, `SELECT CASE WHEN length(CAST(session_id AS BLOB))<=128 THEN session_id END,CASE WHEN length(CAST(state AS BLOB))<=16 THEN state END,sequence FROM task_heads WHERE task_id=?`, task).Scan(&session, &state, &head)
	if err != nil {
		return zero, err
	}
	if !session.Valid || !state.Valid || !sessions.ValidEventPageID(session.String) || head < 1 || head > sessions.MaxTaskEvents {
		return zero, sessions.ErrHistory
	}
	switch state.String {
	case "running", "completed", "failed", "canceled":
	default:
		return zero, sessions.ErrHistory
	}
	rows, err := tx.QueryContext(ctx, `SELECT CASE WHEN length(CAST(e.id AS BLOB))<=128 THEN e.id END,e.sequence,length(CAST(e.body AS BLOB)),l.position,l.event_id
		FROM events e LEFT JOIN event_log l ON l.event_id=e.id AND l.task_id=e.task_id AND l.task_sequence=e.sequence
		WHERE e.task_id=? ORDER BY e.sequence LIMIT 10001`, task)
	if err != nil {
		return zero, err
	}
	type entry struct {
		id             string
		sequence, size int64
		position       int64
	}
	entries := []entry{}
	var total int64
	for rows.Next() {
		var id, ledgerID sql.NullString
		var position sql.NullInt64
		var sequence, size int64
		if err = rows.Scan(&id, &sequence, &size, &position, &ledgerID); err != nil {
			rows.Close()
			return zero, err
		}
		if !id.Valid || !position.Valid || !ledgerID.Valid || id.String != ledgerID.String || !sessions.ValidEventLogEventID(id.String) ||
			sequence != int64(len(entries)+1) || len(entries) >= sessions.MaxTaskEvents || size < 1 || size > int64(sessions.MaxEventPageBytes)-total {
			rows.Close()
			return zero, sessions.ErrHistory
		}
		total += size
		entries = append(entries, entry{id: id.String, sequence: sequence, size: size, position: position.Int64})
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
		ledger, body, readErr := readTaskEventLogByPosition(ctx, tx, entry.position, entry.id)
		if readErr != nil {
			return zero, sessions.ErrHistory
		}
		var event runtime.Event
		if ledger.taskID != task || ledger.taskSequence != entry.sequence || ledger.size != entry.size || json.Unmarshal(body, &event) != nil {
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
	if captured != nil {
		*captured = events
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
