package telemetry

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

const submissionStreamTableSQL = `CREATE TABLE submission_stream_events (
		submission_id TEXT NOT NULL REFERENCES submissions(id),
		sequence INTEGER NOT NULL CHECK(sequence>0),
		event_id TEXT NOT NULL UNIQUE REFERENCES events(id),
		task_id TEXT NOT NULL,
		task_sequence INTEGER NOT NULL CHECK(task_sequence>0),
		body_digest TEXT NOT NULL,
		PRIMARY KEY(submission_id,sequence))`

func migrateSubmissionStream(ctx context.Context, conn *sql.Conn) error {
	create := strings.Replace(submissionStreamTableSQL, "CREATE TABLE", "CREATE TABLE IF NOT EXISTS", 1)
	if _, err := conn.ExecContext(ctx, create); err != nil {
		return err
	}
	var storedSQL string
	if err := conn.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='submission_stream_events'`).Scan(&storedSQL); err != nil || strings.Join(strings.Fields(storedSQL), " ") != strings.Join(strings.Fields(submissionStreamTableSQL), " ") {
		if err != nil {
			return err
		}
		return sessions.ErrEventPage
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM submission_stream_events`); err != nil {
		return err
	}
	rows, err := conn.QueryContext(ctx, `SELECT e.id,e.task_id,e.sequence,length(CAST(e.body AS BLOB)),json_extract(start.body,'$.data.submission_id')
		FROM events e JOIN events start ON start.task_id=e.task_id AND start.sequence=1
		WHERE json_extract(start.body,'$.data.submission_id')!=''
		ORDER BY e.rowid`)
	if err != nil {
		return err
	}
	type entry struct {
		id, task, submission string
		sequence, size       int64
	}
	entries := []entry{}
	for rows.Next() {
		var item entry
		if err := rows.Scan(&item.id, &item.task, &item.sequence, &item.size, &item.submission); err != nil {
			rows.Close()
			return err
		}
		entries = append(entries, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	type taskProjection struct {
		session, state, submission    string
		head, count, minimum, maximum int64
		headKind                      runtime.Kind
	}
	heads := map[string]int64{}
	tasks := map[string]*taskProjection{}
	for _, item := range entries {
		if !sessions.ValidEventPageID(item.id) || !sessions.ValidEventPageID(item.task) || !sessions.ValidEventPageID(item.submission) || item.sequence < 1 || item.size < 1 || item.size > sessions.MaxEventPageBytes {
			return sessions.ErrEventPage
		}
		projection := tasks[item.task]
		if projection == nil {
			projection = &taskProjection{}
			if err := conn.QueryRowContext(ctx, `SELECT session_id,sequence,state FROM task_heads WHERE task_id=?`, item.task).Scan(&projection.session, &projection.head, &projection.state); err != nil {
				return err
			}
			if !sessions.ValidEventPageID(projection.session) || projection.head < 1 {
				return sessions.ErrEventPage
			}
			tasks[item.task] = projection
		}
		var body []byte
		if err := conn.QueryRowContext(ctx, `SELECT body FROM events WHERE id=? AND task_id=? AND sequence=?`, item.id, item.task, item.sequence).Scan(&body); err != nil || int64(len(body)) != item.size {
			if err != nil {
				return err
			}
			return sessions.ErrEventPage
		}
		var event runtime.Event
		if json.Unmarshal(body, &event) != nil || event.Validate() != nil || event.ID != item.id || event.TaskID != item.task || event.Sequence != item.sequence || event.SessionID != projection.session || event.CorrelationID != item.task || (event.Sequence == 1) != (event.Kind == runtime.TaskStarted) || event.Sequence > projection.head {
			return sessions.ErrEventPage
		}
		if event.Sequence == 1 {
			if event.Data.SubmissionID != item.submission {
				return sessions.ErrEventPage
			}
			projection.submission = item.submission
		} else if projection.submission != item.submission {
			return sessions.ErrEventPage
		}
		if event.Sequence < projection.head && !sessions.EventPageStateMatches("running", event.Kind) {
			return sessions.ErrEventPage
		}
		if event.Sequence == projection.head {
			projection.headKind = event.Kind
		}
		projection.count++
		if projection.minimum == 0 || event.Sequence < projection.minimum {
			projection.minimum = event.Sequence
		}
		if event.Sequence > projection.maximum {
			projection.maximum = event.Sequence
		}
		heads[item.submission]++
		digest := sha256.Sum256(body)
		if _, err := conn.ExecContext(ctx, `INSERT INTO submission_stream_events(submission_id,sequence,event_id,task_id,task_sequence,body_digest) VALUES(?,?,?,?,?,?)`, item.submission, heads[item.submission], item.id, item.task, item.sequence, hex.EncodeToString(digest[:])); err != nil {
			return err
		}
	}
	for _, task := range tasks {
		if task.count != task.head || task.minimum != 1 || task.maximum != task.head || !sessions.EventPageStateMatches(task.state, task.headKind) {
			return sessions.ErrEventPage
		}
	}
	_, err = conn.ExecContext(ctx, "PRAGMA user_version=32")
	return err
}
