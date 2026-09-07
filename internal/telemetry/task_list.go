package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

// ListTasks returns newest-first content-free task-head metadata from one
// insertion-fenced read transaction. It does not replay or repair journals.
func (s *Store) ListTasks(ctx context.Context, options sessions.TaskListOptions) (sessions.TaskPage, error) {
	page := sessions.TaskPage{Version: 1, Items: []sessions.TaskSummary{}}
	if options.Validate() != nil {
		return sessions.TaskPage{}, sessions.ErrTaskList
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return sessions.TaskPage{}, err
	}
	defer tx.Rollback()
	cursor := sessions.TaskListCursor{Version: 1, State: options.State}
	var invalidOrdinal, currentHighWater int64
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_heads WHERE rowid<=0),COALESCE(MAX(rowid),0) FROM task_heads`).Scan(&invalidOrdinal, &currentHighWater); err != nil || invalidOrdinal != 0 || currentHighWater < 0 {
		return sessions.TaskPage{}, sessions.ErrTaskList
	}
	if options.After != "" {
		cursor, _ = sessions.DecodeTaskListCursor(options.After)
		if cursor.HighWater > currentHighWater {
			return sessions.TaskPage{}, sessions.ErrTaskList
		}
	} else {
		cursor.HighWater = currentHighWater
	}
	if cursor.HighWater == 0 {
		return page, tx.Commit()
	}
	rows, err := tx.QueryContext(ctx, `SELECT rowid FROM task_heads
	 WHERE rowid<=? AND (?=0 OR rowid<?) AND (?='' OR state=?)
	 ORDER BY rowid DESC LIMIT ?`, cursor.HighWater, cursor.Last, cursor.Last, options.State, options.State, options.Limit+1)
	if err != nil {
		return sessions.TaskPage{}, err
	}
	var rowids []int64
	for rows.Next() {
		var rowid int64
		if err = rows.Scan(&rowid); err != nil {
			rows.Close()
			return sessions.TaskPage{}, err
		}
		if rowid < 1 {
			rows.Close()
			return sessions.TaskPage{}, sessions.ErrTaskList
		}
		rowids = append(rowids, rowid)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return sessions.TaskPage{}, err
	}
	for i, rowid := range rowids {
		if i == options.Limit {
			page.HasMore = true
			break
		}
		item, readErr := readTaskSummary(ctx, tx, rowid)
		if readErr != nil {
			return sessions.TaskPage{}, readErr
		}
		candidate := page
		candidate.Items = append(append([]sessions.TaskSummary{}, page.Items...), item)
		candidate.HasMore = i+1 < len(rowids)
		if candidate.HasMore {
			next := cursor
			next.Last = rowid
			candidate.NextCursor, _ = sessions.EncodeTaskListCursor(next)
		}
		body, marshalErr := json.Marshal(candidate)
		if marshalErr != nil {
			return sessions.TaskPage{}, sessions.ErrTaskList
		}
		if len(body) > sessions.MaxTaskPageBytes {
			if len(page.Items) == 0 {
				return sessions.TaskPage{}, sessions.ErrTaskList
			}
			page.HasMore = true
			break
		}
		page = candidate
		cursor.Last = rowid
	}
	if page.HasMore {
		page.NextCursor, _ = sessions.EncodeTaskListCursor(cursor)
	} else {
		page.NextCursor = ""
	}
	if page.Validate() != nil {
		return sessions.TaskPage{}, sessions.ErrTaskList
	}
	return page, tx.Commit()
}

func readTaskSummary(ctx context.Context, tx *sql.Tx, rowid int64) (sessions.TaskSummary, error) {
	item := sessions.TaskSummary{Version: 1}
	var started, startID, startBodyID, startTask, startSession, startCorrelation, startKind sql.NullString
	var headID, headBodyID, headTask, headSession, headCorrelation, headKind sql.NullString
	var startVersion, startSequence, headVersion, headSequence int64
	err := tx.QueryRowContext(ctx, `SELECT
	 CASE WHEN typeof(h.task_id)='text' AND length(CAST(h.task_id AS BLOB))<=128 THEN h.task_id END,
	 CASE WHEN typeof(h.session_id)='text' AND length(CAST(h.session_id AS BLOB))<=128 THEN h.session_id END,
	 CASE WHEN typeof(h.state)='text' AND length(CAST(h.state AS BLOB))<=16 THEN h.state END,
	 CASE WHEN typeof(h.sequence)='integer' AND h.sequence BETWEEN 1 AND 10000 THEN h.sequence END,
	 CASE WHEN typeof(e.id)='text' AND length(CAST(e.id AS BLOB)) BETWEEN 1 AND 128 THEN e.id END,
	 CASE WHEN typeof(json_extract(e.body,'$.id'))='text' AND length(CAST(json_extract(e.body,'$.id') AS BLOB))<=128 THEN json_extract(e.body,'$.id') END,
	 CASE WHEN typeof(json_extract(e.body,'$.version'))='integer' AND json_extract(e.body,'$.version')=1 THEN json_extract(e.body,'$.version') END,
	 CASE WHEN typeof(json_extract(e.body,'$.sequence'))='integer' AND json_extract(e.body,'$.sequence')=1 THEN json_extract(e.body,'$.sequence') END,
	 CASE WHEN typeof(json_extract(e.body,'$.task_id'))='text' AND length(CAST(json_extract(e.body,'$.task_id') AS BLOB))<=128 THEN json_extract(e.body,'$.task_id') END,
	 CASE WHEN typeof(json_extract(e.body,'$.session_id'))='text' AND length(CAST(json_extract(e.body,'$.session_id') AS BLOB))<=128 THEN json_extract(e.body,'$.session_id') END,
	 CASE WHEN typeof(json_extract(e.body,'$.correlation_id'))='text' AND length(CAST(json_extract(e.body,'$.correlation_id') AS BLOB))<=128 THEN json_extract(e.body,'$.correlation_id') END,
	 CASE WHEN typeof(json_extract(e.body,'$.kind'))='text' AND length(CAST(json_extract(e.body,'$.kind') AS BLOB))<=32 THEN json_extract(e.body,'$.kind') END,
	 CASE WHEN typeof(json_extract(e.body,'$.time'))='text' AND length(CAST(json_extract(e.body,'$.time') AS BLOB))<=64 THEN json_extract(e.body,'$.time') END,
	 CASE WHEN typeof(z.id)='text' AND length(CAST(z.id AS BLOB)) BETWEEN 1 AND 128 THEN z.id END,
	 CASE WHEN typeof(json_extract(z.body,'$.id'))='text' AND length(CAST(json_extract(z.body,'$.id') AS BLOB))<=128 THEN json_extract(z.body,'$.id') END,
	 CASE WHEN typeof(json_extract(z.body,'$.version'))='integer' AND json_extract(z.body,'$.version')=1 THEN json_extract(z.body,'$.version') END,
	 CASE WHEN typeof(json_extract(z.body,'$.sequence'))='integer' AND json_extract(z.body,'$.sequence') BETWEEN 1 AND 10000 THEN json_extract(z.body,'$.sequence') END,
	 CASE WHEN typeof(json_extract(z.body,'$.task_id'))='text' AND length(CAST(json_extract(z.body,'$.task_id') AS BLOB))<=128 THEN json_extract(z.body,'$.task_id') END,
	 CASE WHEN typeof(json_extract(z.body,'$.session_id'))='text' AND length(CAST(json_extract(z.body,'$.session_id') AS BLOB))<=128 THEN json_extract(z.body,'$.session_id') END,
	 CASE WHEN typeof(json_extract(z.body,'$.correlation_id'))='text' AND length(CAST(json_extract(z.body,'$.correlation_id') AS BLOB))<=128 THEN json_extract(z.body,'$.correlation_id') END,
	 CASE WHEN typeof(json_extract(z.body,'$.kind'))='text' AND length(CAST(json_extract(z.body,'$.kind') AS BLOB))<=32 THEN json_extract(z.body,'$.kind') END
	 FROM task_heads h JOIN events e ON e.task_id=h.task_id AND e.sequence=1
	 JOIN events z ON z.task_id=h.task_id AND z.sequence=h.sequence
	 WHERE h.rowid=? AND length(CAST(e.body AS BLOB))<=? AND length(CAST(z.body AS BLOB))<=?`, rowid, sessions.MaxEventPageBytes, sessions.MaxEventPageBytes).Scan(
		&item.TaskID, &item.SessionID, &item.State, &item.Sequence,
		&startID, &startBodyID, &startVersion, &startSequence, &startTask, &startSession, &startCorrelation, &startKind, &started,
		&headID, &headBodyID, &headVersion, &headSequence, &headTask, &headSession, &headCorrelation, &headKind)
	if err != nil {
		if err == sql.ErrNoRows {
			return item, sessions.ErrTaskList
		}
		return item, err
	}
	if !startID.Valid || !startBodyID.Valid || !startTask.Valid || !startSession.Valid || !startCorrelation.Valid || !startKind.Valid || !started.Valid ||
		!headID.Valid || !headBodyID.Valid || !headTask.Valid || !headSession.Valid || !headCorrelation.Valid || !headKind.Valid ||
		startID.String != startBodyID.String || startVersion != 1 || startSequence != 1 || startTask.String != item.TaskID || startSession.String != item.SessionID || startCorrelation.String != item.TaskID || startKind.String != string(runtime.TaskStarted) ||
		headID.String != headBodyID.String || headVersion != 1 || headSequence != item.Sequence || headTask.String != item.TaskID || headSession.String != item.SessionID || headCorrelation.String != item.TaskID || !sessions.EventPageStateMatches(item.State, runtime.Kind(headKind.String)) {
		return item, sessions.ErrTaskList
	}
	item.StartedAt, err = time.Parse(time.RFC3339Nano, started.String)
	if err != nil || item.Validate() != nil {
		return item, sessions.ErrTaskList
	}
	return item, nil
}
