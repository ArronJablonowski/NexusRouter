package telemetry

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

type eventLogEntry struct {
	position, taskSequence, size int64
	eventID, taskID, digest      string
}

// ReadCommittedEventPage returns one frozen, store-wide page. It never invokes
// an EventSink or reconstructs missing runtime events.
func (s *Store) ReadCommittedEventPage(ctx context.Context, options sessions.EventLogOptions) (sessions.CommittedEventPage, error) {
	zero := sessions.CommittedEventPage{}
	if s == nil || ctx == nil || options.Validate() != nil {
		return zero, sessions.ErrEventLog
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	if err := validateEventLogCompleteness(ctx, tx); err != nil {
		return zero, err
	}
	cursor := sessions.EventLogCursor{Version: 1}
	if options.After != "" {
		cursor, _ = sessions.ParseEventLogCursor(options.After)
		if err := validateEventLogAnchor(ctx, tx, cursor.LastPosition, cursor.LastEventID); err != nil {
			return zero, err
		}
		if err := validateEventLogAnchor(ctx, tx, cursor.HighWaterPosition, cursor.HighWaterEventID); err != nil {
			return zero, err
		}
	}
	// An empty start or caught-up cursor snapshots the head seen by this read.
	if options.After == "" || cursor.LastPosition == cursor.HighWaterPosition {
		cursor.HighWaterPosition, cursor.HighWaterEventID, err = eventLogHead(ctx, tx)
		if err != nil {
			return zero, err
		}
		if options.After == "" {
			cursor.LastPosition, cursor.LastEventID = 0, ""
		}
	}
	if cursor.LastPosition > cursor.HighWaterPosition {
		return zero, sessions.ErrEventLog
	}
	entries, overflow, err := readEventLogEntries(ctx, tx, cursor.LastPosition, cursor.HighWaterPosition, options.Limit)
	if err != nil {
		return zero, err
	}
	page := sessions.CommittedEventPage{Version: 1, Events: []sessions.CommittedEvent{}, HasMore: overflow}
	for _, entry := range entries {
		_, body, readErr := readEventLogByPosition(ctx, tx, entry.position, entry.eventID)
		if readErr != nil {
			return zero, readErr
		}
		var event runtime.Event
		if json.Unmarshal(body, &event) != nil {
			return zero, sessions.ErrEventLog
		}
		candidateCursor := cursor
		candidateCursor.LastPosition, candidateCursor.LastEventID = entry.position, entry.eventID
		candidate := page
		candidate.Events = append(append([]sessions.CommittedEvent{}, page.Events...), sessions.CommittedEvent{Version: 1, Position: entry.position, Event: event})
		candidate.HasMore = candidateCursor.LastPosition < candidateCursor.HighWaterPosition
		candidate.NextCursor, _ = sessions.EncodeEventLogCursor(candidateCursor)
		if validateErr := candidate.Validate(); validateErr != nil {
			if len(page.Events) == 0 {
				if validateErr == sessions.ErrEventTooLarge {
					return zero, sessions.ErrEventTooLarge
				}
				return zero, sessions.ErrEventLog
			}
			break
		}
		page, cursor = candidate, candidateCursor
	}
	if len(entries) == 0 && cursor.LastPosition < cursor.HighWaterPosition {
		return zero, sessions.ErrEventLog
	}
	page.HasMore = cursor.LastPosition < cursor.HighWaterPosition
	page.NextCursor, err = sessions.EncodeEventLogCursor(cursor)
	if err != nil || page.Validate() != nil {
		return zero, sessions.ErrEventLog
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return page, nil
}

func eventLogHead(ctx context.Context, tx *sql.Tx) (int64, string, error) {
	var position int64
	var id string
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(max(position),0) FROM event_log`).Scan(&position)
	if err != nil || position == 0 {
		return position, "", err
	}
	if err = tx.QueryRowContext(ctx, `SELECT event_id FROM event_log WHERE position=?`, position).Scan(&id); err != nil || !sessions.ValidEventLogEventID(id) {
		return 0, "", sessions.ErrEventLog
	}
	return position, id, nil
}

func validateEventLogAnchor(ctx context.Context, tx *sql.Tx, position int64, id string) error {
	if position == 0 && id == "" {
		return nil
	}
	_, _, err := readEventLogByPosition(ctx, tx, position, id)
	return err
}

func readEventLogEntries(ctx context.Context, tx *sql.Tx, after, highWater int64, limit int) ([]eventLogEntry, bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT l.position,l.event_id,l.task_id,l.task_sequence,l.body_digest,length(CAST(e.body AS BLOB))
		FROM event_log l JOIN events e ON e.id=l.event_id AND e.task_id=l.task_id AND e.sequence=l.task_sequence
		WHERE position>? AND position<=? ORDER BY position LIMIT ?`, after, highWater, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	entries := []eventLogEntry{}
	total := int64(0)
	expected := after + 1
	for rows.Next() {
		var entry eventLogEntry
		if err := rows.Scan(&entry.position, &entry.eventID, &entry.taskID, &entry.taskSequence, &entry.digest, &entry.size); err != nil {
			return nil, false, err
		}
		if len(entries) == limit || total+entry.size > sessions.MaxCommittedEventPageBytes {
			if len(entries) == 0 && entry.size > sessions.MaxCommittedEventPageBytes {
				return nil, false, sessions.ErrEventTooLarge
			}
			return entries, true, rows.Err()
		}
		if entry.position != expected || !sessions.ValidEventLogEventID(entry.eventID) || !sessions.ValidEventPageID(entry.taskID) || entry.taskSequence < 1 || entry.size < 1 || !submissionDigest(entry.digest) {
			return nil, false, sessions.ErrEventLog
		}
		total += entry.size
		entries = append(entries, entry)
		expected++
	}
	return entries, false, rows.Err()
}

func readEventLogByPosition(ctx context.Context, tx *sql.Tx, position int64, id string) (eventLogEntry, []byte, error) {
	var entry eventLogEntry
	var session string
	var body []byte
	err := tx.QueryRowContext(ctx, `SELECT l.position,l.event_id,l.task_id,l.task_sequence,l.body_digest,e.body,h.session_id
		FROM event_log l JOIN events e ON e.id=l.event_id AND e.task_id=l.task_id AND e.sequence=l.task_sequence
		JOIN task_heads h ON h.task_id=l.task_id WHERE l.position=? AND l.event_id=?`, position, id).
		Scan(&entry.position, &entry.eventID, &entry.taskID, &entry.taskSequence, &entry.digest, &body, &session)
	if err != nil || entry.position < 1 || !sessions.ValidEventLogEventID(entry.eventID) || !sessions.ValidEventPageID(entry.taskID) || !sessions.ValidEventPageID(session) || entry.taskSequence < 1 || len(body) < 1 || len(body) > sessions.MaxCommittedEventPageBytes || !submissionDigest(entry.digest) || streamBodyDigest(body) != entry.digest {
		if len(body) > sessions.MaxCommittedEventPageBytes {
			return eventLogEntry{}, nil, sessions.ErrEventTooLarge
		}
		return eventLogEntry{}, nil, sessions.ErrEventLog
	}
	var event runtime.Event
	if json.Unmarshal(body, &event) != nil || event.Validate() != nil || event.ID != entry.eventID || event.TaskID != entry.taskID || event.SessionID != session || event.Sequence != entry.taskSequence {
		return eventLogEntry{}, nil, sessions.ErrEventLog
	}
	canonical, encodeErr := event.Encode()
	if encodeErr != nil || !bytes.Equal(canonical, body) {
		return eventLogEntry{}, nil, sessions.ErrEventLog
	}
	entry.size = int64(len(body))
	return entry, body, nil
}

func validateEventLogCompleteness(ctx context.Context, tx *sql.Tx) error {
	var schema int
	var tableSQL string
	if err := tx.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&schema); err != nil || schema != currentStorageSchema {
		return sessions.ErrEventLog
	}
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='event_log'`).Scan(&tableSQL); err != nil || strings.Join(strings.Fields(tableSQL), " ") != strings.Join(strings.Fields(eventLogTableSQL), " ") {
		return sessions.ErrEventLog
	}
	var events, indexed, minimum, maximum, broken, brokenTasks, orphanEvents, reordered int64
	err := tx.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM events),(SELECT count(*) FROM event_log),
		(SELECT COALESCE(min(position),0) FROM event_log),(SELECT COALESCE(max(position),0) FROM event_log),
		(SELECT count(*) FROM event_log l LEFT JOIN events e ON e.id=l.event_id AND e.task_id=l.task_id AND e.sequence=l.task_sequence WHERE e.id IS NULL),
		(SELECT count(*) FROM task_heads h LEFT JOIN (
		 SELECT task_id,count(*) AS amount,min(sequence) AS minimum,max(sequence) AS maximum FROM events GROUP BY task_id
		) e ON e.task_id=h.task_id WHERE h.sequence<1 OR e.amount IS NULL OR e.amount!=h.sequence OR e.minimum!=1 OR e.maximum!=h.sequence),
		(SELECT count(*) FROM events e LEFT JOIN task_heads h ON h.task_id=e.task_id WHERE h.task_id IS NULL),
		(SELECT count(*) FROM (
		 SELECT task_sequence,row_number() OVER (PARTITION BY task_id ORDER BY position) AS expected FROM event_log
		) WHERE task_sequence!=expected)`).Scan(&events, &indexed, &minimum, &maximum, &broken, &brokenTasks, &orphanEvents, &reordered)
	if err != nil {
		return err
	}
	if events < 0 || indexed != events || broken != 0 || brokenTasks != 0 || orphanEvents != 0 || reordered != 0 || indexed == 0 && (minimum != 0 || maximum != 0) || indexed > 0 && (minimum != 1 || maximum != indexed) {
		return sessions.ErrEventLog
	}
	return nil
}
