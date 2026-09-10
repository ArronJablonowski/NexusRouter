package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

type attentionPageCursor struct {
	Version   int    `json:"version"`
	BoardID   string `json:"board_id"`
	ExpiresAt int64  `json:"expires_at"`
	ClaimID   string `json:"claim_id"`
}

type attentionPageCandidate struct {
	attentionCandidate
	expiresAt int64
}

// ListClaimAttentionPage provides internal keyset traversal for the bounded
// recovery supervisor. Cursors are process-epoch scoped: after restart the
// supervisor safely starts a new traversal from the first durable row.
func (s *Store) ListClaimAttentionPage(ctx context.Context, boardID, after string, limit int) ([]workboard.ClaimAttention, string, error) {
	if !validWorkboardID(boardID) || limit < 1 || limit > workboard.MaxAttentionScanClaims {
		return nil, "", invalidWorkboard("attention_list")
	}
	cursor, err := s.decodeAttentionPageCursor(after)
	if err != nil || after != "" && cursor.BoardID != boardID {
		return nil, "", ErrWorkboardCursor
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	if _, _, _, err = readBoardRow(ctx, tx, boardID); err != nil {
		return nil, "", err
	}
	rows, err := tx.QueryContext(ctx, `SELECT card_id,attempt_id,id,expires_at FROM workboard_claims
		WHERE board_id=? AND state='attention' AND (expires_at>? OR (expires_at=? AND id>?))
		ORDER BY expires_at,id LIMIT ?`, boardID, cursor.ExpiresAt, cursor.ExpiresAt, cursor.ClaimID, limit+1)
	if err != nil {
		return nil, "", err
	}
	ids := make([]attentionPageCandidate, 0, limit+1)
	for rows.Next() {
		var item attentionPageCandidate
		if err = rows.Scan(&item.cardID, &item.attemptID, &item.claimID, &item.expiresAt); err != nil {
			rows.Close()
			return nil, "", err
		}
		ids = append(ids, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, "", err
	}
	hasMore := len(ids) > limit
	if hasMore {
		ids = ids[:limit]
	}
	items := make([]workboard.ClaimAttention, 0, len(ids))
	for _, id := range ids {
		lease, _, readErr := readLifecycleClaim(ctx, tx, boardID, id.cardID, id.attemptID, id.claimID)
		if readErr != nil {
			return nil, "", readErr
		}
		event, readErr := scanCanonicalWorkboardEvent(tx.QueryRowContext(ctx, `SELECT id,sequence,operation_id,kind,actor_id,actor_type,card_id,created_at,body
			FROM workboard_events WHERE board_id=? AND card_id=? AND kind=? ORDER BY sequence DESC LIMIT 1`, boardID, id.cardID, string(workboard.ClaimAttentionAction)), boardID)
		if readErr != nil || event.CardID != id.cardID || event.Kind != workboard.ClaimAttentionAction {
			if errors.Is(readErr, sql.ErrNoRows) {
				return nil, "", ErrWorkboardCorrupt
			}
			if readErr != nil {
				return nil, "", readErr
			}
			return nil, "", ErrWorkboardCorrupt
		}
		reason := workboard.AttentionStale
		if !event.CreatedAt.Before(lease.ExpiresAt) {
			reason = workboard.AttentionExpired
		}
		item := workboard.ClaimAttention{Version: 1, BoardID: boardID, CardID: lease.CardID, AttemptID: lease.AttemptID,
			ClaimID: lease.ClaimID, ClaimRevision: lease.Revision, OwnerID: lease.OwnerID, Reason: reason,
			ObservedAt: event.CreatedAt, ExpiresAt: lease.ExpiresAt}
		if item.Validate() != nil {
			return nil, "", ErrWorkboardCorrupt
		}
		items = append(items, item)
	}
	next := ""
	if hasMore {
		last := ids[len(ids)-1]
		next, err = s.encodeAttentionPageCursor(attentionPageCursor{Version: 1, BoardID: boardID,
			ExpiresAt: last.expiresAt, ClaimID: last.claimID})
		if err != nil {
			return nil, "", err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, "", err
	}
	return items, next, nil
}

func (s *Store) encodeAttentionPageCursor(cursor attentionPageCursor) (string, error) {
	body, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return s.signWorkboardCursor(body)
}

func (s *Store) decodeAttentionPageCursor(value string) (attentionPageCursor, error) {
	if value == "" {
		return attentionPageCursor{}, nil
	}
	if len(value) > workboard.MaxCursorBytes {
		return attentionPageCursor{}, ErrWorkboardCursor
	}
	body, err := s.verifyWorkboardCursor(value)
	if err != nil {
		return attentionPageCursor{}, ErrWorkboardCursor
	}
	var cursor attentionPageCursor
	if strictJSON(body, &cursor) != nil || cursor.Version != 1 || !validWorkboardID(cursor.BoardID) ||
		cursor.ExpiresAt < 0 || !validWorkboardID(cursor.ClaimID) {
		return attentionPageCursor{}, ErrWorkboardCursor
	}
	canonical, _ := s.encodeAttentionPageCursor(cursor)
	if canonical != value {
		return attentionPageCursor{}, ErrWorkboardCursor
	}
	return cursor, nil
}
