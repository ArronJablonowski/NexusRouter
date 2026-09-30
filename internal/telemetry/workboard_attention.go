package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/ArronJablonowski/NexusRouter/workboard"
)

type attentionCandidate struct {
	cardID, attemptID, claimID string
}

// ObserveClaimAttention derives supervision state exclusively from durable
// claim observations. It never releases a lease, reassigns work, or authorizes
// replay of a side effect.
func (s *Store) ObserveClaimAttention(ctx context.Context, observation workboard.AttentionScan) ([]workboard.ClaimAttention, error) {
	if observation.Validate() != nil {
		return nil, invalidWorkboard("attention_scan")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err = reserveWorkboardWriter(ctx, tx); err != nil {
		return nil, err
	}
	board, _, _, err := readBoardRow(ctx, tx, observation.BoardID)
	if err != nil {
		return nil, err
	}
	if board.State != "active" {
		return nil, &workboard.Violation{Code: workboard.CodeIllegalTransition, Field: "board_state"}
	}
	candidates, err := readAttentionCandidates(ctx, tx, observation)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return []workboard.ClaimAttention{}, nil
	}
	operationID := newWorkboardID()
	if operationID == "" {
		return nil, errors.New("secure identifier generation failed")
	}
	firstSequence := board.EventSequence + 1
	durableBytes := 0
	results := make([]workboard.ClaimAttention, 0, len(candidates))
	for _, candidate := range candidates {
		result, event, changedBytes, applyErr := markObservedClaimAttention(ctx, tx, board, candidate, observation, operationID)
		if applyErr != nil {
			return nil, applyErr
		}
		board.EventSequence = event.Sequence
		durableBytes += changedBytes
		results = append(results, result)
	}
	board.Revision++
	board.UpdatedAt = observation.ObservedAt
	boardBody, err := json.Marshal(board)
	if err != nil || board.Validate() != nil {
		return nil, ErrWorkboardCorrupt
	}
	requestDigest, err := digestJSON(observation)
	if err != nil {
		return nil, err
	}
	receipt := workboard.OperationReceipt{Version: 1, BoardID: board.ID, OperationID: operationID, RequestDigest: requestDigest,
		FirstSequence: firstSequence, LastSequence: board.EventSequence, EventCount: len(results), BoardRevision: board.Revision,
		Outcome: "committed", CreatedAt: observation.ObservedAt}
	response, err := finalizeWorkboardReceipt(&receipt, durableBytes+len(boardBody))
	if err != nil {
		return nil, err
	}
	updated, err := tx.ExecContext(ctx, `UPDATE workboard_boards SET revision=?,event_sequence=?,updated_at=?,body=? WHERE id=? AND revision=?`,
		board.Revision, board.EventSequence, board.UpdatedAt.UnixNano(), boardBody, board.ID, board.Revision-1)
	if err != nil {
		return nil, err
	}
	if changed, _ := updated.RowsAffected(); changed != 1 {
		return nil, &workboard.Violation{Code: workboard.CodeStaleRevision, Field: "board_revision"}
	}
	if err = insertWorkboardOperation(ctx, tx, "board", board.ID, requestDigest, receipt, response); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return results, nil
}

func readAttentionCandidates(ctx context.Context, tx *sql.Tx, observation workboard.AttentionScan) ([]attentionCandidate, error) {
	rows, err := tx.QueryContext(ctx, `SELECT card_id,attempt_id,id FROM workboard_claims
		WHERE board_id=? AND state='active' AND (expires_at<=? OR last_heartbeat<=?) ORDER BY expires_at,id LIMIT ?`,
		observation.BoardID, observation.ObservedAt.UnixNano(), observation.StaleBefore.UnixNano(), observation.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]attentionCandidate, 0, observation.Limit)
	for rows.Next() {
		var item attentionCandidate
		if err = rows.Scan(&item.cardID, &item.attemptID, &item.claimID); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func markObservedClaimAttention(ctx context.Context, tx *sql.Tx, board workboard.Board, candidate attentionCandidate,
	observation workboard.AttentionScan, operationID string) (workboard.ClaimAttention, workboard.BoardEvent, int, error) {
	lease, stored, err := readLifecycleClaim(ctx, tx, board.ID, candidate.cardID, candidate.attemptID, candidate.claimID)
	if err != nil {
		return workboard.ClaimAttention{}, workboard.BoardEvent{}, 0, err
	}
	next, err := workboard.MarkAttentionFromObservation(lease, lease.Revision, observation.ObservedAt, observation.StaleBefore)
	if err != nil {
		return workboard.ClaimAttention{}, workboard.BoardEvent{}, 0, err
	}
	before := stored
	stored = updateStoredClaim(stored, next)
	reason := workboard.AttentionStale
	if !observation.ObservedAt.Before(next.ExpiresAt) {
		reason = workboard.AttentionExpired
	}
	body, err := encodeLifecycle(stored, 16<<10)
	if err != nil {
		return workboard.ClaimAttention{}, workboard.BoardEvent{}, 0, err
	}
	updated, err := tx.ExecContext(ctx, `UPDATE workboard_claims SET revision=?,state=?,body=? WHERE id=? AND revision=? AND state='active'`,
		stored.Revision, stored.State, body, stored.ID, before.Revision)
	if err != nil {
		return workboard.ClaimAttention{}, workboard.BoardEvent{}, 0, err
	}
	if changed, _ := updated.RowsAffected(); changed != 1 {
		return workboard.ClaimAttention{}, workboard.BoardEvent{}, 0, &workboard.Violation{Code: workboard.CodeStaleRevision, Field: "claim_revision"}
	}
	mutation := workboard.LifecycleMutation{BoardID: board.ID, CardID: stored.CardID, AttemptID: stored.AttemptID, ClaimID: stored.ID}
	attemptBytes, err := updateAttemptClaim(ctx, tx, mutation, before, stored)
	if err != nil {
		return workboard.ClaimAttention{}, workboard.BoardEvent{}, 0, err
	}
	event := workboard.BoardEvent{Version: 1, ID: newWorkboardID(), BoardID: board.ID, Sequence: board.EventSequence + 1,
		OperationID: operationID, Kind: workboard.ClaimAttentionAction, ActorID: observation.Actor.ID, ActorType: observation.Actor.Type,
		CardID: stored.CardID, CreatedAt: observation.ObservedAt}
	eventBody, err := json.Marshal(event)
	if event.ID == "" || err != nil || event.Validate() != nil {
		return workboard.ClaimAttention{}, workboard.BoardEvent{}, 0, ErrWorkboardCorrupt
	}
	if err = insertWorkboardEvent(ctx, tx, event.ID, event.BoardID, event.Sequence, event.OperationID, string(event.Kind), event.CardID, observation.Actor, observation.ObservedAt, eventBody); err != nil {
		return workboard.ClaimAttention{}, workboard.BoardEvent{}, 0, err
	}
	result := workboard.ClaimAttention{Version: 1, BoardID: board.ID, CardID: next.CardID, AttemptID: next.AttemptID,
		ClaimID: next.ClaimID, ClaimRevision: next.Revision, OwnerID: next.OwnerID, Reason: reason,
		ObservedAt: observation.ObservedAt, ExpiresAt: next.ExpiresAt}
	if result.Validate() != nil {
		return workboard.ClaimAttention{}, workboard.BoardEvent{}, 0, ErrWorkboardCorrupt
	}
	return result, event, len(body) + attemptBytes + len(eventBody), nil
}

func (s *Store) ListClaimAttention(ctx context.Context, boardID string, limit int) ([]workboard.ClaimAttention, error) {
	items, _, err := s.ListClaimAttentionPage(ctx, boardID, "", limit)
	return items, err
}
