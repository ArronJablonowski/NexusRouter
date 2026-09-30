package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

// ReadEventPage observes the head and contiguous event slice in one SQLite read
// snapshot. Length probes precede body reads, including the overflow candidate.
func (s *Store) ReadEventPage(ctx context.Context, task string, after int64, limit int) (sessions.EventPage, error) {
	p := sessions.EventPage{Version: 1, TaskID: task, FromSequence: after, NextSequence: after, Events: []runtime.Event{}}
	if after < 0 {
		return p, sessions.ErrEventCursor
	}
	if !sessions.ValidEventPageID(task) || limit < 1 || limit > 100 {
		return p, sessions.ErrEventPage
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return p, err
	}
	defer tx.Rollback()
	var session, state sql.NullString
	if err = tx.QueryRowContext(ctx, "SELECT CASE WHEN length(CAST(session_id AS BLOB))<=128 THEN session_id END,CASE WHEN length(CAST(state AS BLOB))<=16 THEN state END,sequence FROM task_heads WHERE task_id=?", task).Scan(&session, &state, &p.HeadSequence); err != nil {
		return p, err
	}
	if !session.Valid || !state.Valid {
		return p, sessions.ErrEventPage
	}
	p.SessionID, p.State = session.String, state.String
	if p.HeadSequence < 1 || !sessions.ValidEventPageID(p.SessionID) {
		return p, sessions.ErrEventPage
	}
	if after > p.HeadSequence {
		return p, sessions.ErrEventCursor
	}
	// Prove the complete head even for an empty page. Bound its extra read
	// independently before loading any payload into the process.
	var headID sql.NullString
	var headSize int64
	err = tx.QueryRowContext(ctx, "SELECT CASE WHEN length(CAST(id AS BLOB))<=128 THEN id END,length(CAST(body AS BLOB)) FROM events WHERE task_id=? AND sequence=?", task, p.HeadSequence).Scan(&headID, &headSize)
	if err != nil || !headID.Valid || headID.String == "" || headSize < 1 {
		return p, sessions.ErrEventPage
	}
	if headSize > sessions.MaxEventPageBytes {
		return p, sessions.ErrEventTooLarge
	}
	var headBody []byte
	if err := tx.QueryRowContext(ctx, "SELECT body FROM events WHERE task_id=? AND sequence=?", task, p.HeadSequence).Scan(&headBody); err != nil {
		return p, sessions.ErrEventPage
	}
	var head runtime.Event
	if int64(len(headBody)) != headSize || json.Unmarshal(headBody, &head) != nil || head.Validate() != nil || headID.String != head.ID || head.TaskID != task || head.SessionID != p.SessionID || head.CorrelationID != task || head.Sequence != p.HeadSequence || !sessions.EventPageStateMatches(p.State, head.Kind) || (head.Sequence == 1) != (head.Kind == runtime.TaskStarted) {
		return p, sessions.ErrEventPage
	}
	headBody = nil
	rows, err := tx.QueryContext(ctx, "SELECT CASE WHEN length(CAST(id AS BLOB))<=128 THEN id END,sequence,length(CAST(body AS BLOB)) FROM events WHERE task_id=? AND sequence>? AND sequence<=? ORDER BY sequence LIMIT ?", task, after, p.HeadSequence, limit)
	if err != nil {
		return p, err
	}
	type entry struct {
		id             string
		sequence, size int64
	}
	entries := []entry{}
	for rows.Next() {
		var e entry
		var id sql.NullString
		if err := rows.Scan(&id, &e.sequence, &e.size); err != nil {
			rows.Close()
			return p, err
		}
		if !id.Valid || id.String == "" {
			rows.Close()
			return p, sessions.ErrEventPage
		}
		e.id = id.String
		entries = append(entries, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return p, err
	}
	total := int64(0)
	overflow := false
	for _, entry := range entries {
		if entry.sequence != p.NextSequence+1 || entry.size < 1 {
			return p, sessions.ErrEventPage
		}
		if entry.size > int64(sessions.MaxEventPageBytes)-total {
			if len(p.Events) == 0 {
				return p, sessions.ErrEventTooLarge
			}
			overflow = true
			break
		}
		var body []byte
		if err := tx.QueryRowContext(ctx, "SELECT body FROM events WHERE task_id=? AND sequence=? AND id=?", task, entry.sequence, entry.id).Scan(&body); err != nil {
			return p, sessions.ErrEventPage
		}
		var event runtime.Event
		if int64(len(body)) != entry.size || json.Unmarshal(body, &event) != nil || event.ID != entry.id {
			return p, sessions.ErrEventPage
		}
		p.Events = append(p.Events, event)
		p.NextSequence = entry.sequence
		total += entry.size
	}
	if !overflow && len(entries) < limit && p.NextSequence < p.HeadSequence {
		return p, sessions.ErrEventPage
	}
	p.HasMore = p.NextSequence < p.HeadSequence
	if err := p.Validate(); err != nil {
		return p, err
	}
	if err := tx.Commit(); err != nil {
		return p, err
	}
	return p, nil
}
