package telemetry

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

// ListSessionTasks returns newest-first, content-free task and lineage metadata
// for one session from a single insertion-fenced read transaction.
func (s *Store) ListSessionTasks(ctx context.Context, session string, options sessions.SessionTaskListOptions) (sessions.SessionTaskPage, error) {
	page := sessions.SessionTaskPage{Version: 1, SessionID: session, Items: []sessions.SessionTask{}}
	if s == nil || options.Validate(session) != nil {
		return sessions.SessionTaskPage{}, sessions.ErrSessionTasks
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return sessions.SessionTaskPage{}, err
	}
	defer tx.Rollback()
	cursor := sessions.SessionTaskCursor{Version: 1, SessionID: session}
	var invalidOrdinal, currentHighWater int64
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_heads WHERE session_id=? AND rowid<=0),COALESCE(MAX(rowid),0) FROM task_heads WHERE session_id=?`, session, session).Scan(&invalidOrdinal, &currentHighWater); err != nil || invalidOrdinal != 0 || currentHighWater < 0 {
		return sessions.SessionTaskPage{}, sessions.ErrSessionTasks
	}
	if options.After != "" {
		cursor, _ = sessions.DecodeSessionTaskCursor(options.After)
		if cursor.HighWater > currentHighWater {
			return sessions.SessionTaskPage{}, sessions.ErrSessionTasks
		}
	} else {
		cursor.HighWater = currentHighWater
	}
	if cursor.HighWater == 0 {
		if page.Validate() != nil {
			return sessions.SessionTaskPage{}, sessions.ErrSessionTasks
		}
		return page, tx.Commit()
	}
	rows, err := tx.QueryContext(ctx, `SELECT rowid FROM task_heads
	 WHERE session_id=? AND rowid<=? AND (?=0 OR rowid<?)
	 ORDER BY rowid DESC LIMIT ?`, session, cursor.HighWater, cursor.Last, cursor.Last, options.Limit+1)
	if err != nil {
		return sessions.SessionTaskPage{}, err
	}
	var rowids []int64
	for rows.Next() {
		var rowid int64
		if err = rows.Scan(&rowid); err != nil || rowid < 1 {
			rows.Close()
			return sessions.SessionTaskPage{}, sessions.ErrSessionTasks
		}
		rowids = append(rowids, rowid)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return sessions.SessionTaskPage{}, err
	}
	for i, rowid := range rowids {
		if i == options.Limit {
			page.HasMore = true
			break
		}
		item, readErr := readSessionTask(ctx, tx, rowid, session)
		if readErr != nil {
			return sessions.SessionTaskPage{}, readErr
		}
		candidate := page
		candidate.Items = append(append([]sessions.SessionTask{}, page.Items...), item)
		candidate.HasMore = i+1 < len(rowids)
		if candidate.HasMore {
			next := cursor
			next.Last = rowid
			candidate.NextCursor, _ = sessions.EncodeSessionTaskCursor(next)
		}
		body, marshalErr := json.Marshal(candidate)
		if marshalErr != nil {
			return sessions.SessionTaskPage{}, sessions.ErrSessionTasks
		}
		if len(body) > sessions.MaxSessionTaskPageBytes {
			if len(page.Items) == 0 {
				return sessions.SessionTaskPage{}, sessions.ErrSessionTasks
			}
			page.HasMore = true
			break
		}
		page = candidate
		cursor.Last = rowid
	}
	if page.HasMore {
		page.NextCursor, _ = sessions.EncodeSessionTaskCursor(cursor)
	} else {
		page.NextCursor = ""
	}
	if page.Validate() != nil {
		return sessions.SessionTaskPage{}, sessions.ErrSessionTasks
	}
	return page, tx.Commit()
}

func readSessionTask(ctx context.Context, tx *sql.Tx, rowid int64, session string) (sessions.SessionTask, error) {
	summary, start, head, err := readCanonicalSessionTask(ctx, tx, rowid)
	if err != nil || summary.SessionID != session {
		return sessions.SessionTask{}, sessions.ErrSessionTasks
	}
	item := sessions.SessionTask{Version: 1, TaskID: summary.TaskID, SessionID: summary.SessionID, State: summary.State, Sequence: summary.Sequence, StartedAt: summary.StartedAt, Fence: sessions.TaskHeadFence{Version: 1, TaskID: summary.TaskID, SessionID: summary.SessionID, HeadSequence: summary.Sequence, HeadEventID: head.ID}}
	item.ParentTaskID, item.RetryOfTaskID = start.Data.ParentTaskID, start.Data.RetryOfTaskID
	if item.Validate() != nil || !sessionTaskParentExists(ctx, tx, item.ParentTaskID, session, rowid) || !sessionTaskRetryValid(ctx, tx, item.TaskID, item.RetryOfTaskID, rowid) {
		return sessions.SessionTask{}, sessions.ErrSessionTasks
	}
	return item, nil
}

func sessionTaskParentExists(ctx context.Context, tx *sql.Tx, task, session string, childRowID int64) bool {
	if task == "" {
		return true
	}
	var rowid int64
	if tx.QueryRowContext(ctx, `SELECT rowid FROM task_heads WHERE task_id=? AND session_id=?`, task, session).Scan(&rowid) != nil || rowid < 1 || rowid >= childRowID {
		return false
	}
	related, _, _, err := readCanonicalSessionTask(ctx, tx, rowid)
	return err == nil && related.TaskID == task && related.SessionID == session
}

func sessionTaskRetryValid(ctx context.Context, tx *sql.Tx, task, predecessor string, childRowID int64) bool {
	if predecessor == "" {
		return true
	}
	return validateRetryChainBefore(ctx, tx, task, predecessor, childRowID) == nil
}

func readCanonicalSessionTask(ctx context.Context, tx *sql.Tx, rowid int64) (sessions.TaskSummary, runtime.Event, runtime.Event, error) {
	summary, err := readTaskSummary(ctx, tx, rowid)
	if err != nil {
		return sessions.TaskSummary{}, runtime.Event{}, runtime.Event{}, err
	}
	var startID, headID string
	var startRaw, headRaw []byte
	err = tx.QueryRowContext(ctx, `SELECT
	 CASE WHEN length(CAST(s.id AS BLOB)) BETWEEN 1 AND 128 THEN s.id END,s.body,
	 CASE WHEN length(CAST(z.id AS BLOB)) BETWEEN 1 AND 128 THEN z.id END,z.body
	 FROM events s JOIN events z ON z.task_id=s.task_id
	 WHERE s.task_id=? AND s.sequence=1 AND z.sequence=?
	 AND length(CAST(s.body AS BLOB))<=? AND length(CAST(z.body AS BLOB))<=?`, summary.TaskID, summary.Sequence, sessions.MaxEventPageBytes, sessions.MaxEventPageBytes).Scan(&startID, &startRaw, &headID, &headRaw)
	if err != nil {
		return sessions.TaskSummary{}, runtime.Event{}, runtime.Event{}, sessions.ErrSessionTasks
	}
	start, ok := canonicalSessionTaskEvent(startRaw, summary.TaskID, summary.SessionID, 1)
	if !ok || start.ID != startID || start.Kind != runtime.TaskStarted || !start.Time.Equal(summary.StartedAt) {
		return sessions.TaskSummary{}, runtime.Event{}, runtime.Event{}, sessions.ErrSessionTasks
	}
	head, ok := canonicalSessionTaskEvent(headRaw, summary.TaskID, summary.SessionID, summary.Sequence)
	if !ok || head.ID != headID || !sessions.EventPageStateMatches(summary.State, head.Kind) {
		return sessions.TaskSummary{}, runtime.Event{}, runtime.Event{}, sessions.ErrSessionTasks
	}
	return summary, start, head, nil
}

func canonicalSessionTaskEvent(raw []byte, task, session string, sequence int64) (runtime.Event, bool) {
	var event runtime.Event
	if json.Unmarshal(raw, &event) != nil || event.Validate() != nil || event.TaskID != task || event.SessionID != session || event.CorrelationID != task || event.Sequence != sequence {
		return runtime.Event{}, false
	}
	canonical, err := event.Encode()
	return event, err == nil && bytes.Equal(canonical, raw)
}
