package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
	"github.com/ArronJablonowski/DarwinRouter/submissions"
)

const maxSubmissionStreamEvents = 1_000_000

type submissionStreamEntry struct {
	sequence, taskSequence, size     int64
	eventID, taskID, session, digest string
}

// ReadSubmissionStreamPage observes status and the immutable, append-time
// submission order in one SQLite snapshot. Metadata and sizes are validated
// before bounded bodies are loaded.
func (s *Store) ReadSubmissionStreamPage(ctx context.Context, id string, after int64, limit int) (submissions.StreamPage, error) {
	p := submissions.StreamPage{Version: 1, SubmissionID: id, FromSequence: after, NextSequence: after, Events: []submissions.StreamEvent{}}
	if !sessions.ValidEventPageID(id) || after < 0 || limit < 1 || limit > 100 {
		return p, submissions.ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return p, err
	}
	defer tx.Rollback()
	p.Status, err = readSubmission(ctx, tx, id)
	if err != nil {
		return p, err
	}
	var count, minimum int64
	err = tx.QueryRowContext(ctx, `SELECT count(*),COALESCE(min(sequence),0),COALESCE(max(sequence),0) FROM submission_stream_events WHERE submission_id=?`, id).Scan(&count, &minimum, &p.EventHeadSequence)
	if err != nil {
		return p, err
	}
	if count < 0 || count > maxSubmissionStreamEvents || count != p.EventHeadSequence || count > 0 && minimum != 1 {
		return p, sessions.ErrEventPage
	}
	// Interleaving different tasks is allowed, but each task's rows must retain
	// its journal order inside the immutable submission-wide sequence.
	var causallyReordered int64
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM (
		SELECT task_sequence,row_number() OVER (PARTITION BY task_id ORDER BY sequence) AS expected
		FROM submission_stream_events WHERE submission_id=?
	) WHERE task_sequence!=expected`, id).Scan(&causallyReordered)
	if err != nil || causallyReordered != 0 {
		if err != nil {
			return p, err
		}
		return p, sessions.ErrEventPage
	}
	// The mapping must cover every event whose immutable task start binds it to
	// this submission. This catches missing/relinked rows before any SSE bytes.
	var linked int64
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM events e JOIN events start ON start.task_id=e.task_id AND start.sequence=1
		WHERE json_extract(start.body,'$.kind')='task.started' AND json_extract(start.body,'$.data.submission_id')=?`, id).Scan(&linked)
	if err != nil || linked != count {
		if err != nil {
			return p, err
		}
		return p, sessions.ErrEventPage
	}
	if err := validateSubmissionStreamTasks(ctx, tx, p.Status); err != nil {
		return p, err
	}
	var brokenTasks int64
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM task_heads h JOIN (
		SELECT task_id,count(*) AS amount,min(sequence) AS minimum,max(sequence) AS maximum FROM events GROUP BY task_id
	) e ON e.task_id=h.task_id WHERE h.task_id IN (
		SELECT DISTINCT task_id FROM submission_stream_events WHERE submission_id=?
	) AND (e.amount!=h.sequence OR e.minimum!=1 OR e.maximum!=h.sequence)`, id).Scan(&brokenTasks)
	if err != nil || brokenTasks != 0 {
		if err != nil {
			return p, err
		}
		return p, sessions.ErrEventPage
	}
	terminal := p.Status.State == "succeeded" || p.Status.State == "failed" || p.Status.State == "canceled"
	if terminal {
		p.ResultSequence = p.EventHeadSequence + 1
	}
	max := p.EventHeadSequence
	if p.ResultSequence > 0 {
		max = p.ResultSequence
	}
	if after > max {
		return p, sessions.ErrEventCursor
	}
	if after > p.EventHeadSequence {
		return p, tx.Commit()
	}
	if after > 0 {
		if err := validateSubmissionStreamCursor(ctx, tx, id, after); err != nil {
			return p, err
		}
	}
	entries, overflow, err := submissionStreamEntries(ctx, tx, id, after, limit)
	if err != nil {
		return p, err
	}
	for _, entry := range entries {
		var body []byte
		err = tx.QueryRowContext(ctx, `SELECT body FROM events WHERE id=? AND task_id=? AND sequence=?`, entry.eventID, entry.taskID, entry.taskSequence).Scan(&body)
		if err != nil || int64(len(body)) != entry.size || streamBodyDigest(body) != entry.digest {
			return p, sessions.ErrEventPage
		}
		var event runtime.Event
		if json.Unmarshal(body, &event) != nil || event.Validate() != nil || event.ID != entry.eventID || event.TaskID != entry.taskID || event.Sequence != entry.taskSequence || event.SessionID != entry.session || event.CorrelationID != entry.taskID {
			return p, sessions.ErrEventPage
		}
		p.Events = append(p.Events, submissions.StreamEvent{Sequence: entry.sequence, Event: event})
		p.NextSequence = entry.sequence
	}
	p.HasMoreEvents = overflow || p.NextSequence < p.EventHeadSequence
	if len(p.Events) == 0 && after < p.EventHeadSequence {
		return p, sessions.ErrEventPage
	}
	if err := tx.Commit(); err != nil {
		return p, err
	}
	return p, nil
}

// validateSubmissionStreamTasks proves that the mapping's task set is exactly
// the submission's task set and that every mapped task has a canonical start
// binding. Once the submission is terminal, every mapped task must also have a
// terminal durable head; otherwise the virtual result marker would overtake
// unfinished work.
func validateSubmissionStreamTasks(ctx context.Context, tx *sql.Tx, status submissions.Status) error {
	expected := make(map[string]bool, len(status.TaskIDs))
	for _, task := range status.TaskIDs {
		if !sessions.ValidEventPageID(task) || expected[task] {
			return sessions.ErrEventPage
		}
		expected[task] = true
	}
	var mappedTasks int
	if err := tx.QueryRowContext(ctx, `SELECT count(DISTINCT task_id) FROM submission_stream_events WHERE submission_id=?`, status.ID).Scan(&mappedTasks); err != nil {
		return err
	}
	if mappedTasks != len(expected) {
		return sessions.ErrEventPage
	}
	rows, err := tx.QueryContext(ctx, `SELECT mapped.task_id,h.session_id,h.sequence,h.state,length(CAST(start.body AS BLOB)),m.body_digest
		FROM (SELECT DISTINCT task_id FROM submission_stream_events WHERE submission_id=?) mapped
		JOIN task_heads h ON h.task_id=mapped.task_id
		JOIN events start ON start.task_id=mapped.task_id AND start.sequence=1
		JOIN submission_stream_events m ON m.submission_id=? AND m.event_id=start.id AND m.task_id=start.task_id AND m.task_sequence=1
		ORDER BY mapped.task_id`, status.ID, status.ID)
	if err != nil {
		return err
	}
	terminal := status.State == "succeeded" || status.State == "failed" || status.State == "canceled"
	seen := make(map[string]bool, len(expected))
	states := make(map[string]string, len(expected))
	type taskEntry struct {
		task, session, state, digest string
		head, startSize              int64
	}
	tasks := make([]taskEntry, 0, len(expected))
	for rows.Next() {
		var task, session, state, digest string
		var head, size int64
		if err := rows.Scan(&task, &session, &head, &state, &size, &digest); err != nil {
			rows.Close()
			return err
		}
		if !expected[task] || seen[task] || !sessions.ValidEventPageID(session) || head < 1 || size < 1 || size > sessions.MaxEventPageBytes || !submissionDigest(digest) {
			rows.Close()
			if size > sessions.MaxEventPageBytes {
				return sessions.ErrEventTooLarge
			}
			return sessions.ErrEventPage
		}
		seen[task] = true
		states[task] = state
		tasks = append(tasks, taskEntry{task: task, session: session, state: state, digest: digest, head: head, startSize: size})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(seen) != len(expected) {
		return sessions.ErrEventPage
	}
	for _, task := range tasks {
		var body []byte
		if err := tx.QueryRowContext(ctx, `SELECT body FROM events WHERE task_id=? AND sequence=1`, task.task).Scan(&body); err != nil || int64(len(body)) != task.startSize || streamBodyDigest(body) != task.digest {
			return sessions.ErrEventPage
		}
		var start runtime.Event
		if json.Unmarshal(body, &start) != nil || start.Validate() != nil || start.Kind != runtime.TaskStarted || start.TaskID != task.task || start.SessionID != task.session || start.CorrelationID != task.task || start.Sequence != 1 || start.Data.SubmissionID != status.ID {
			return sessions.ErrEventPage
		}
	}
	if !terminal {
		return nil
	}
	for _, task := range tasks {
		var terminalSize int64
		if err := tx.QueryRowContext(ctx, `SELECT length(CAST(body AS BLOB)) FROM events WHERE task_id=? AND sequence=?`, task.task, task.head).Scan(&terminalSize); err != nil || terminalSize < 1 || terminalSize > sessions.MaxEventPageBytes {
			if terminalSize > sessions.MaxEventPageBytes {
				return sessions.ErrEventTooLarge
			}
			return sessions.ErrEventPage
		}
		var terminalBody []byte
		if err := tx.QueryRowContext(ctx, `SELECT body FROM events WHERE task_id=? AND sequence=?`, task.task, task.head).Scan(&terminalBody); err != nil || int64(len(terminalBody)) != terminalSize {
			return sessions.ErrEventPage
		}
		var end runtime.Event
		if json.Unmarshal(terminalBody, &end) != nil || end.Validate() != nil || end.TaskID != task.task || end.SessionID != task.session || end.CorrelationID != task.task || end.Sequence != task.head || !sessions.EventPageStateMatches(task.state, end.Kind) || task.state == "running" {
			return sessions.ErrEventPage
		}
	}
	if len(expected) == 0 {
		if status.Result != nil {
			return sessions.ErrEventPage
		}
		if status.State == "failed" && validSubmissionStreamFailure(status.ErrorCode) || status.State == "canceled" && (status.ErrorCode == "canceled" || status.CancelRequested && status.ErrorCode == "") {
			return nil
		}
		return sessions.ErrEventPage
	}
	if status.State == "succeeded" {
		if status.Result == nil || !expected[status.Result.TaskID] || states[status.Result.TaskID] != "completed" || status.ErrorCode != "" || status.Result.Turns < 1 {
			return sessions.ErrEventPage
		}
	}
	return nil
}

func validSubmissionStreamFailure(code string) bool {
	switch code {
	case "task_failed", "execution_failed", "canceled", "interrupted", "lease_lost", "admission_denied", "deadline_exceeded", "persistence_failed", "recovery_exhausted":
		return true
	}
	return false
}

func validateSubmissionStreamCursor(ctx context.Context, tx *sql.Tx, id string, sequence int64) error {
	var eventID, taskID, session, digest string
	var taskSequence, size int64
	err := tx.QueryRowContext(ctx, `SELECT m.event_id,m.task_id,m.task_sequence,h.session_id,m.body_digest,length(CAST(e.body AS BLOB))
		FROM submission_stream_events m JOIN events e ON e.id=m.event_id AND e.task_id=m.task_id AND e.sequence=m.task_sequence
		JOIN task_heads h ON h.task_id=m.task_id WHERE m.submission_id=? AND m.sequence=?`, id, sequence).Scan(&eventID, &taskID, &taskSequence, &session, &digest, &size)
	if err != nil || !sessions.ValidEventPageID(eventID) || !sessions.ValidEventPageID(taskID) || !sessions.ValidEventPageID(session) || !submissionDigest(digest) || taskSequence < 1 || size < 1 || size > sessions.MaxEventPageBytes {
		if err != nil {
			return err
		}
		if size > sessions.MaxEventPageBytes {
			return sessions.ErrEventTooLarge
		}
		return sessions.ErrEventPage
	}
	var body []byte
	if err := tx.QueryRowContext(ctx, `SELECT body FROM events WHERE id=?`, eventID).Scan(&body); err != nil || int64(len(body)) != size || streamBodyDigest(body) != digest {
		return sessions.ErrEventPage
	}
	var event runtime.Event
	if json.Unmarshal(body, &event) != nil || event.Validate() != nil || event.ID != eventID || event.TaskID != taskID || event.Sequence != taskSequence || event.SessionID != session || event.CorrelationID != taskID {
		return sessions.ErrEventPage
	}
	return nil
}

func submissionStreamEntries(ctx context.Context, tx *sql.Tx, id string, after int64, limit int) ([]submissionStreamEntry, bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT m.sequence,m.event_id,m.task_id,m.task_sequence,h.session_id,m.body_digest,length(CAST(e.body AS BLOB))
		FROM submission_stream_events m JOIN events e ON e.id=m.event_id AND e.task_id=m.task_id AND e.sequence=m.task_sequence
		JOIN task_heads h ON h.task_id=m.task_id
		WHERE m.submission_id=? AND m.sequence>? ORDER BY m.sequence LIMIT ?`, id, after, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	entries := []submissionStreamEntry{}
	total, overflow := int64(0), false
	for rows.Next() {
		var entry submissionStreamEntry
		if err := rows.Scan(&entry.sequence, &entry.eventID, &entry.taskID, &entry.taskSequence, &entry.session, &entry.digest, &entry.size); err != nil {
			return nil, false, err
		}
		if len(entries) == limit {
			overflow = true
			break
		}
		if entry.sequence != after+int64(len(entries))+1 || !sessions.ValidEventPageID(entry.eventID) || !sessions.ValidEventPageID(entry.taskID) || entry.taskSequence < 1 || !submissionDigest(entry.digest) || entry.size < 1 {
			return nil, false, sessions.ErrEventPage
		}
		if entry.size > sessions.MaxEventPageBytes {
			if len(entries) == 0 {
				return nil, false, sessions.ErrEventTooLarge
			}
			overflow = true
			break
		}
		if total+entry.size > sessions.MaxEventPageBytes {
			overflow = true
			break
		}
		total += entry.size
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return entries, overflow, nil
}
