package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

type workboardEventCursor struct {
	Version int    `json:"v"`
	BoardID string `json:"b"`
	High    int64  `json:"h"`
	After   int64  `json:"a"`
}

// ListWorkboardEvents returns a stable high-water page of canonical, redacted
// workboard events. Event bodies deliberately contain attribution and routing
// metadata only; request payloads and idempotency keys are never returned.
func (s *Store) ListWorkboardEvents(ctx context.Context, boardID string, options workboard.BoardEventOptions) (workboard.BoardEventPage, error) {
	if !validWorkboardID(boardID) || options.Validate() != nil {
		return workboard.BoardEventPage{}, invalidWorkboard("events")
	}
	cursor, err := s.decodeWorkboardEventCursor(options.After)
	if err != nil || options.After != "" && cursor.BoardID != boardID {
		return workboard.BoardEventPage{}, ErrWorkboardCursor
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return workboard.BoardEventPage{}, err
	}
	defer tx.Rollback()
	board, _, _, err := readBoardRow(ctx, tx, boardID)
	if err != nil {
		return workboard.BoardEventPage{}, err
	}
	tailMode := options.TailAfterSequence != 0
	var anchor workboard.BoardEvent
	if tailMode {
		if options.TailAfterSequence > board.EventSequence {
			return workboard.BoardEventPage{}, ErrWorkboardCursor
		}
		cursor = workboardEventCursor{Version: 1, BoardID: boardID, High: board.EventSequence, After: options.TailAfterSequence}
		anchor, err = scanCanonicalWorkboardEvent(tx.QueryRowContext(ctx, `SELECT id,sequence,operation_id,kind,actor_id,actor_type,card_id,created_at,body
			FROM workboard_events WHERE board_id=? AND sequence=?`, boardID, cursor.After), boardID)
		if err != nil {
			if err == sql.ErrNoRows {
				return workboard.BoardEventPage{}, ErrWorkboardCorrupt
			}
			return workboard.BoardEventPage{}, err
		}
		if anchor.Sequence != cursor.After {
			return workboard.BoardEventPage{}, ErrWorkboardCorrupt
		}
	} else if options.After == "" {
		cursor = workboardEventCursor{Version: 1, BoardID: boardID, High: board.EventSequence}
	} else if cursor.High > board.EventSequence {
		return workboard.BoardEventPage{}, ErrWorkboardCursor
	}
	var count, minimum, maximum int64
	if err = tx.QueryRowContext(ctx, `SELECT count(*),COALESCE(min(sequence),0),COALESCE(max(sequence),0)
		FROM workboard_events WHERE board_id=? AND sequence>? AND sequence<=?`, boardID, rangeStart(cursor, tailMode), cursor.High).Scan(&count, &minimum, &maximum); err != nil {
		return workboard.BoardEventPage{}, err
	}
	expectedCount := cursor.High
	expectedMinimum := int64(1)
	if tailMode {
		expectedCount = cursor.High - cursor.After
		expectedMinimum = cursor.After + 1
	}
	if count != expectedCount || count > 0 && (minimum != expectedMinimum || maximum != cursor.High) || count == 0 && (minimum != 0 || maximum != 0) {
		return workboard.BoardEventPage{}, ErrWorkboardCorrupt
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,sequence,operation_id,kind,actor_id,actor_type,card_id,created_at,body
		FROM workboard_events WHERE board_id=? AND sequence>? AND sequence<=? ORDER BY sequence LIMIT ?`, boardID, cursor.After, cursor.High, options.Limit+1)
	if err != nil {
		return workboard.BoardEventPage{}, err
	}
	defer rows.Close()
	items := make([]workboard.BoardEvent, 0, options.Limit+1)
	for rows.Next() {
		event, scanErr := scanCanonicalWorkboardEvent(rows, boardID)
		if scanErr != nil {
			return workboard.BoardEventPage{}, scanErr
		}
		if event.Sequence != cursor.After+int64(len(items))+1 || event.Sequence > cursor.High {
			return workboard.BoardEventPage{}, ErrWorkboardCorrupt
		}
		items = append(items, event)
	}
	if err = rows.Err(); err != nil {
		return workboard.BoardEventPage{}, err
	}
	if tailMode && len(items) == 0 {
		items = append(items, anchor)
	}
	page := workboard.BoardEventPage{Version: 1, BoardID: boardID, HighWaterSequence: cursor.High, Items: items}
	if !tailMode || items[0].Sequence != cursor.After {
		if len(items) > options.Limit {
			page.Items = items[:options.Limit]
			page.HasMore = true
			cursor.After = page.Items[len(page.Items)-1].Sequence
			page.NextCursor, err = s.encodeWorkboardEventCursor(cursor)
			if err != nil {
				return workboard.BoardEventPage{}, err
			}
		}
	}
	if page.Validate() != nil {
		return workboard.BoardEventPage{}, ErrWorkboardCorrupt
	}
	if err = tx.Commit(); err != nil {
		return workboard.BoardEventPage{}, err
	}
	return page, nil
}

func rangeStart(cursor workboardEventCursor, tailMode bool) int64 {
	if tailMode {
		return cursor.After
	}
	return 0
}

func scanCanonicalWorkboardEvent(row rowScanner, boardID string) (workboard.BoardEvent, error) {
	var indexed workboard.BoardEvent
	var kind string
	var cardID sql.NullString
	var created int64
	var body []byte
	indexed.BoardID = boardID
	if err := row.Scan(&indexed.ID, &indexed.Sequence, &indexed.OperationID, &kind, &indexed.ActorID, &indexed.ActorType, &cardID, &created, &body); err != nil {
		return workboard.BoardEvent{}, err
	}
	if cardID.Valid {
		indexed.CardID = cardID.String
	}
	indexed.Version, indexed.Kind, indexed.CreatedAt = 1, workboard.BoardAction(kind), time.Unix(0, created).UTC()
	var event workboard.BoardEvent
	if len(body) == 0 || len(body) > workboard.MaxTransactionBytes || strictJSON(body, &event) != nil || event.Validate() != nil ||
		event.Version != indexed.Version || event.ID != indexed.ID || event.BoardID != indexed.BoardID || event.Sequence != indexed.Sequence ||
		event.OperationID != indexed.OperationID || event.Kind != indexed.Kind || event.ActorID != indexed.ActorID ||
		event.ActorType != indexed.ActorType || event.CardID != indexed.CardID || !event.CreatedAt.Equal(indexed.CreatedAt) {
		return workboard.BoardEvent{}, ErrWorkboardCorrupt
	}
	return event, nil
}

func (s *Store) encodeWorkboardEventCursor(cursor workboardEventCursor) (string, error) {
	body, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return s.signWorkboardCursor(body)
}

func (s *Store) decodeWorkboardEventCursor(value string) (workboardEventCursor, error) {
	if value == "" {
		return workboardEventCursor{}, nil
	}
	if len(value) > workboard.MaxCursorBytes {
		return workboardEventCursor{}, ErrWorkboardCursor
	}
	body, err := s.verifyWorkboardCursor(value)
	if err != nil {
		return workboardEventCursor{}, ErrWorkboardCursor
	}
	var cursor workboardEventCursor
	if strictJSON(body, &cursor) != nil || cursor.Version != 1 || !validWorkboardID(cursor.BoardID) || cursor.High < 1 || cursor.After < 1 || cursor.After >= cursor.High {
		return workboardEventCursor{}, ErrWorkboardCursor
	}
	canonical, err := s.encodeWorkboardEventCursor(cursor)
	if err != nil || canonical != value {
		return workboardEventCursor{}, ErrWorkboardCursor
	}
	return cursor, nil
}
