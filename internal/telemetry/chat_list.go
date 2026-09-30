package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/ArronJablonowski/NexusRouter/sessions"
)

// ListChats returns one newest-first representative per durable session. The
// high-water fence fixes both membership and each session's representative for
// the lifetime of a cursor.
func (s *Store) ListChats(ctx context.Context, options sessions.ChatListOptions) (sessions.ChatPage, error) {
	page := sessions.ChatPage{Version: 1, Items: []sessions.ChatSummary{}}
	if s == nil || options.Validate() != nil {
		return sessions.ChatPage{}, sessions.ErrTaskList
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return sessions.ChatPage{}, err
	}
	defer tx.Rollback()
	cursor := sessions.ChatListCursor{Version: 1}
	var highWater int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(rowid),0) FROM task_heads`).Scan(&highWater); err != nil || highWater < 0 {
		return sessions.ChatPage{}, sessions.ErrTaskList
	}
	if options.After != "" {
		cursor, _ = sessions.DecodeChatListCursor(options.After)
		if cursor.HighWater > highWater {
			return sessions.ChatPage{}, sessions.ErrTaskList
		}
	} else {
		cursor.HighWater = highWater
	}
	if cursor.HighWater == 0 {
		return page, tx.Commit()
	}
	rows, err := tx.QueryContext(ctx, `SELECT MAX(rowid) FROM task_heads
		WHERE rowid<=? GROUP BY session_id HAVING (?=0 OR MAX(rowid)<?)
		ORDER BY MAX(rowid) DESC LIMIT ?`, cursor.HighWater, cursor.Last, cursor.Last, options.Limit+1)
	if err != nil {
		return sessions.ChatPage{}, err
	}
	rowids := []int64{}
	for rows.Next() {
		var rowid int64
		if rows.Scan(&rowid) != nil || rowid < 1 {
			rows.Close()
			return sessions.ChatPage{}, sessions.ErrTaskList
		}
		rowids = append(rowids, rowid)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return sessions.ChatPage{}, err
	}
	for index, rowid := range rowids {
		if index == options.Limit {
			page.HasMore = true
			break
		}
		task, readErr := readTaskSummary(ctx, tx, rowid)
		if readErr != nil {
			return sessions.ChatPage{}, readErr
		}
		item := sessions.ChatSummary{Version: 1, ChatID: task.SessionID, LatestTaskID: task.TaskID, State: task.State, Revision: task.Sequence, StartedAt: task.StartedAt}
		candidate := page
		candidate.Items = append(append([]sessions.ChatSummary{}, page.Items...), item)
		candidate.HasMore = index+1 < len(rowids)
		if candidate.HasMore {
			next := cursor
			next.Last = rowid
			candidate.NextCursor, _ = sessions.EncodeChatListCursor(next)
		}
		body, marshalErr := json.Marshal(candidate)
		if marshalErr != nil || len(body) > sessions.MaxChatPageBytes {
			if len(page.Items) == 0 {
				return sessions.ChatPage{}, sessions.ErrTaskList
			}
			page.HasMore = true
			break
		}
		page, cursor.Last = candidate, rowid
	}
	if page.HasMore {
		page.NextCursor, _ = sessions.EncodeChatListCursor(cursor)
	} else {
		page.NextCursor = ""
	}
	if page.Validate() != nil {
		return sessions.ChatPage{}, sessions.ErrTaskList
	}
	return page, tx.Commit()
}
