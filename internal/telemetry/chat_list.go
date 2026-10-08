package telemetry

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/ArronJablonowski/NexusRouter/sessions"
)

// ListChats returns one newest-first representative per durable session. The
// high-water fence fixes both membership and each session's representative for
// the lifetime of a cursor.
func (s *Store) ListChats(ctx context.Context, options sessions.ChatListOptions) (sessions.ChatPage, error) {
	return s.ListChatsWithPreferences(ctx, options, nil)
}

func (s *Store) ListChatsWithPreferences(ctx context.Context, options sessions.ChatListOptions, preferences map[string]sessions.ChatPreference) (sessions.ChatPage, error) {
	pinned := []string{}
	for id, p := range preferences {
		if p.Pinned {
			pinned = append(pinned, id)
		}
	}
	sort.Strings(pinned)
	pins, _ := json.Marshal(pinned)
	digest := ""
	if len(pinned) > 0 {
		digest = fmt.Sprintf("%x", sha256.Sum256(pins))
	}
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
		if cursor.HighWater > highWater || cursor.OrderDigest != digest {
			return sessions.ChatPage{}, sessions.ErrTaskList
		}
	} else {
		cursor.HighWater = highWater
		cursor.OrderDigest = digest
	}
	if cursor.HighWater == 0 {
		return page, tx.Commit()
	}
	rows, err := tx.QueryContext(ctx, `WITH chats AS (
 SELECT MAX(rowid) AS rid, session_id FROM task_heads WHERE rowid<=? GROUP BY session_id
 ), ordered AS (SELECT rid, session_id IN (SELECT value FROM json_each(?)) AS pinned FROM chats)
 SELECT rid,pinned FROM ordered WHERE (?=0 OR pinned<? OR (pinned=? AND rid<?))
 ORDER BY pinned DESC,rid DESC LIMIT ?`, cursor.HighWater, string(pins), cursor.Last, cursor.LastPinned, cursor.LastPinned, cursor.Last, options.Limit+1)
	if err != nil {
		return sessions.ChatPage{}, err
	}
	rowids := []int64{}
	pinStates := []bool{}
	for rows.Next() {
		var rowid int64
		var pinned bool
		if rows.Scan(&rowid, &pinned) != nil || rowid < 1 {
			rows.Close()
			return sessions.ChatPage{}, sessions.ErrTaskList
		}
		rowids = append(rowids, rowid)
		pinStates = append(pinStates, pinned)
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
		preference := preferences[task.SessionID]
		item.Title, item.Pinned, item.PreferenceRevision = preference.Title, preference.Pinned, preference.Revision
		candidate := page
		candidate.Items = append(append([]sessions.ChatSummary{}, page.Items...), item)
		candidate.HasMore = index+1 < len(rowids)
		if candidate.HasMore {
			next := cursor
			next.Last = rowid
			next.LastPinned = pinStates[index]
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
		cursor.LastPinned = pinStates[index]
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

func (s *Store) ChatExists(ctx context.Context, id string) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM task_heads WHERE session_id=?)", id).Scan(&exists)
	return exists, err
}
