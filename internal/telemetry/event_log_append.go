package telemetry

import (
	"bytes"
	"context"
	"database/sql"
	"math"
	"strings"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func appendEventLog(ctx context.Context, tx *sql.Tx, event runtime.Event, body []byte) error {
	if err := validateEventLogAppend(event, body); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO event_log(event_id,task_id,task_sequence,body_digest) VALUES(?,?,?,?)`, event.ID, event.TaskID, event.Sequence, streamBodyDigest(body))
	return err
}

func validateEventLogAppend(event runtime.Event, body []byte) error {
	if event.Validate() != nil || !sessions.ValidEventLogEventID(event.ID) || !sessions.ValidEventPageID(event.TaskID) || !sessions.ValidEventPageID(event.SessionID) {
		return sessions.ErrEventLog
	}
	canonical, err := event.Encode()
	if err != nil || !bytes.Equal(canonical, body) {
		return sessions.ErrEventLog
	}
	highWaterID := strings.Repeat("<", 128)
	if event.ID == highWaterID {
		highWaterID = strings.Repeat(">", 128)
	}
	cursor := sessions.EventLogCursor{Version: 1, LastPosition: math.MaxInt64 - 1, LastEventID: event.ID, HighWaterPosition: math.MaxInt64, HighWaterEventID: highWaterID}
	encoded, err := sessions.EncodeEventLogCursor(cursor)
	if err != nil {
		return sessions.ErrEventLog
	}
	page := sessions.CommittedEventPage{Version: 1, Events: []sessions.CommittedEvent{{Version: 1, Position: cursor.LastPosition, Event: event}}, NextCursor: encoded, HasMore: true}
	if err := page.Validate(); err != nil {
		if err == sessions.ErrEventTooLarge {
			return sessions.ErrEventTooLarge
		}
		return sessions.ErrEventLog
	}
	return nil
}

func validateEventLogRetry(ctx context.Context, tx *sql.Tx, event runtime.Event, body []byte) error {
	var task, digest string
	var position, sequence int64
	err := tx.QueryRowContext(ctx, `SELECT position,task_id,task_sequence,body_digest FROM event_log WHERE event_id=?`, event.ID).Scan(&position, &task, &sequence, &digest)
	if err != nil || position < 1 || task != event.TaskID || sequence != event.Sequence || digest != streamBodyDigest(body) {
		return ErrConflict
	}
	return nil
}
