package telemetry

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

const eventLogTableSQL = `CREATE TABLE event_log (
		position INTEGER PRIMARY KEY AUTOINCREMENT CHECK(position>0),
		event_id TEXT NOT NULL UNIQUE,
		task_id TEXT NOT NULL,
		task_sequence INTEGER NOT NULL CHECK(task_sequence>0),
		body_digest TEXT NOT NULL)`

func migrateEventLog(ctx context.Context, conn *sql.Conn) error {
	create := strings.Replace(eventLogTableSQL, "CREATE TABLE", "CREATE TABLE IF NOT EXISTS", 1)
	if _, err := conn.ExecContext(ctx, create); err != nil {
		return err
	}
	var storedSQL string
	if err := conn.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='event_log'`).Scan(&storedSQL); err != nil || strings.Join(strings.Fields(storedSQL), " ") != strings.Join(strings.Fields(eventLogTableSQL), " ") {
		if err != nil {
			return err
		}
		return sessions.ErrEventPage
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM event_log; DELETE FROM sqlite_sequence WHERE name='event_log'`); err != nil {
		return err
	}
	var broken int64
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM task_heads h LEFT JOIN (
		SELECT task_id,count(*) AS amount,min(sequence) AS minimum,max(sequence) AS maximum FROM events GROUP BY task_id
	) e ON e.task_id=h.task_id WHERE h.sequence<1 OR e.amount IS NULL OR e.amount!=h.sequence OR e.minimum!=1 OR e.maximum!=h.sequence`).Scan(&broken); err != nil || broken != 0 {
		if err != nil {
			return err
		}
		return sessions.ErrEventPage
	}
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM (
		SELECT sequence,row_number() OVER (PARTITION BY task_id ORDER BY rowid) AS expected FROM events
	) WHERE sequence!=expected`).Scan(&broken); err != nil || broken != 0 {
		if err != nil {
			return err
		}
		return sessions.ErrEventPage
	}
	type entry struct {
		id, task, session, state string
		rowid, sequence, head    int64
		body                     []byte
	}
	var lastRowID int64
	for {
		var item entry
		var size int64
		err := conn.QueryRowContext(ctx, `SELECT e.rowid,e.id,e.task_id,e.sequence,length(CAST(e.body AS BLOB)),h.session_id,h.sequence,h.state
			FROM events e JOIN task_heads h ON h.task_id=e.task_id WHERE e.rowid>? ORDER BY e.rowid LIMIT 1`, lastRowID).
			Scan(&item.rowid, &item.id, &item.task, &item.sequence, &size, &item.session, &item.head, &item.state)
		if err == sql.ErrNoRows {
			break
		}
		if err != nil {
			return err
		}
		if size > sessions.MaxEventPageBytes {
			return sessions.ErrEventTooLarge
		}
		if item.rowid <= lastRowID || !sessions.ValidEventLogEventID(item.id) || !sessions.ValidEventPageID(item.task) || !sessions.ValidEventPageID(item.session) || item.sequence < 1 || item.sequence > item.head || size < 1 {
			return sessions.ErrEventPage
		}
		if err := conn.QueryRowContext(ctx, `SELECT body FROM events WHERE rowid=? AND id=?`, item.rowid, item.id).Scan(&item.body); err != nil || int64(len(item.body)) != size {
			if err != nil {
				return err
			}
			return sessions.ErrEventPage
		}
		var event runtime.Event
		if json.Unmarshal(item.body, &event) != nil || event.Validate() != nil || event.ID != item.id || event.TaskID != item.task || event.SessionID != item.session || event.Sequence != item.sequence || (event.Sequence == 1) != (event.Kind == runtime.TaskStarted) {
			return sessions.ErrEventPage
		}
		canonical, encodeErr := event.Encode()
		if encodeErr != nil || !bytes.Equal(canonical, item.body) {
			return sessions.ErrEventPage
		}
		// Schema 33 never skips or rewrites an incompatible legacy event. The
		// operator must restore or repair the source database before retrying;
		// this transaction preserves the prior schema and rows on failure.
		if err := validateEventLogAppend(event, item.body); err != nil {
			return err
		}
		if item.sequence == item.head && !sessions.EventPageStateMatches(item.state, event.Kind) || item.sequence < item.head && !sessions.EventPageStateMatches("running", event.Kind) {
			return sessions.ErrEventPage
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO event_log(event_id,task_id,task_sequence,body_digest) VALUES(?,?,?,?)`, item.id, item.task, item.sequence, streamBodyDigest(item.body)); err != nil {
			return err
		}
		lastRowID = item.rowid
	}
	var events, indexed, minimum, maximum int64
	if err := conn.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM events),(SELECT count(*) FROM event_log),COALESCE(min(position),0),COALESCE(max(position),0) FROM event_log`).Scan(&events, &indexed, &minimum, &maximum); err != nil || events != indexed || indexed == 0 && (minimum != 0 || maximum != 0) || indexed > 0 && (minimum != 1 || maximum != indexed) {
		if err != nil {
			return err
		}
		return sessions.ErrEventPage
	}
	_, err := conn.ExecContext(ctx, `PRAGMA user_version=33`)
	return err
}
