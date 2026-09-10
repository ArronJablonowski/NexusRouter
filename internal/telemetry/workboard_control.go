package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

func (s *Store) ApplyControlMutation(ctx context.Context, mutation workboard.ControlMutation) (workboard.OperationReceipt, error) {
	if err := validateControlMutation(mutation, true); err != nil {
		return workboard.OperationReceipt{}, err
	}
	requestDigest, err := workboard.ControlDigest(mutation)
	if err != nil || requestDigest != mutation.RequestDigest {
		return workboard.OperationReceipt{}, invalidWorkboard("request_digest")
	}
	keyDigest := digestBytes([]byte(mutation.IdempotencyKey))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return workboard.OperationReceipt{}, err
	}
	defer tx.Rollback()
	if err = reserveWorkboardWriter(ctx, tx); err != nil {
		return workboard.OperationReceipt{}, err
	}
	if receipt, found, replayErr := readWorkboardReceipt(ctx, tx, "board", mutation.BoardID, keyDigest, requestDigest); found || replayErr != nil {
		return receipt, replayErr
	}
	board, _, _, err := readBoardRow(ctx, tx, mutation.BoardID)
	if err != nil {
		return workboard.OperationReceipt{}, err
	}
	if board.State != "active" {
		return workboard.OperationReceipt{}, &workboard.Violation{Code: workboard.CodeIllegalTransition, Field: "board_state"}
	}
	card, body, err := readStoredCard(ctx, tx, mutation.BoardID, mutation.CardID)
	if err != nil {
		return workboard.OperationReceipt{}, err
	}
	var claimRevision *int64
	var durableBytes int
	switch mutation.Kind {
	case workboard.ControlPauseRequest, workboard.ControlCancelRequest:
		claimRevision, durableBytes, err = requestStoredControl(ctx, tx, mutation, &card, &body)
	case workboard.ControlBlock, workboard.ControlUnblock:
		var revision int64
		revision, durableBytes, err = transitionStoredBlock(ctx, tx, mutation, &card, &body)
		claimRevision = &revision
	case workboard.ControlCancelFinalize:
		var revision int64
		revision, durableBytes, err = finalizeStoredCancel(ctx, tx, mutation, &board, &card, &body)
		claimRevision = &revision
	}
	if err != nil {
		return workboard.OperationReceipt{}, err
	}
	operationID, eventID := newWorkboardID(), newWorkboardID()
	if operationID == "" || eventID == "" {
		return workboard.OperationReceipt{}, errors.New("secure identifier generation failed")
	}
	board.Revision++
	board.EventSequence++
	board.UpdatedAt = mutation.Now
	boardBody, err := json.Marshal(board)
	if err != nil || board.Validate() != nil {
		return workboard.OperationReceipt{}, ErrWorkboardCorrupt
	}
	event := workboard.BoardEvent{Version: 1, ID: eventID, BoardID: board.ID, Sequence: board.EventSequence, OperationID: operationID,
		Kind: controlAction(mutation.Kind), ActorID: mutation.Actor.ID, ActorType: mutation.Actor.Type, CardID: card.ID, CreatedAt: mutation.Now}
	eventBody, err := json.Marshal(event)
	if err != nil || event.Validate() != nil {
		return workboard.OperationReceipt{}, ErrWorkboardCorrupt
	}
	cardRevision := card.Revision
	receipt := workboard.OperationReceipt{Version: 1, BoardID: board.ID, OperationID: operationID, RequestDigest: requestDigest,
		FirstSequence: board.EventSequence, LastSequence: board.EventSequence, EventCount: 1, BoardRevision: board.Revision,
		CardID: card.ID, CardRevision: &cardRevision, ClaimRevision: claimRevision, Outcome: "committed", CreatedAt: mutation.Now}
	response, err := finalizeWorkboardReceipt(&receipt, durableBytes+len(boardBody)+len(eventBody))
	if err != nil {
		return workboard.OperationReceipt{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE workboard_boards SET revision=?,layout_revision=?,event_sequence=?,active_claims=?,updated_at=?,body=? WHERE id=? AND revision=?`,
		board.Revision, board.LayoutRevision, board.EventSequence, board.ActiveClaims, board.UpdatedAt.UnixNano(), boardBody, board.ID, board.Revision-1)
	if err != nil {
		return workboard.OperationReceipt{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return workboard.OperationReceipt{}, &workboard.Violation{Code: workboard.CodeStaleRevision, Field: "board_revision"}
	}
	if err = insertWorkboardOperation(ctx, tx, "board", board.ID, keyDigest, receipt, response); err != nil {
		return workboard.OperationReceipt{}, err
	}
	if err = insertWorkboardEvent(ctx, tx, eventID, board.ID, board.EventSequence, operationID, string(event.Kind), card.ID, mutation.Actor, mutation.Now, eventBody); err != nil {
		return workboard.OperationReceipt{}, err
	}
	if err = tx.Commit(); err != nil {
		return workboard.OperationReceipt{}, err
	}
	return receipt, nil
}

func requestStoredControl(ctx context.Context, tx *sql.Tx, mutation workboard.ControlMutation, card *workboard.Card, body *storedWorkboardCard) (*int64, int, error) {
	if card.Revision != mutation.ExpectedCardRevision {
		return nil, 0, &workboard.Violation{Code: workboard.CodeStaleRevision, Field: "card_revision"}
	}
	if card.State != workboard.InProgress && card.State != workboard.Blocked || card.CurrentClaimID == "" || body.CurrentAttemptID == "" {
		return nil, 0, &workboard.Violation{Code: workboard.CodeIllegalTransition, Field: "control"}
	}
	if mutation.Kind == workboard.ControlPauseRequest {
		if card.PauseRequested {
			return nil, 0, &workboard.Violation{Code: workboard.CodeInvalid, Field: "pause_requested"}
		}
		card.PauseRequested = true
	} else {
		if card.CancelRequested {
			return nil, 0, &workboard.Violation{Code: workboard.CodeInvalid, Field: "cancel_requested"}
		}
		card.CancelRequested = true
	}
	lease, _, err := readLifecycleClaim(ctx, tx, card.BoardID, card.ID, body.CurrentAttemptID, card.CurrentClaimID)
	if err != nil {
		return nil, 0, err
	}
	card.Revision++
	card.UpdatedAt = mutation.Now
	bytes, err := writeLifecycleCard(ctx, tx, *card, *body, mutation.ExpectedCardRevision)
	_ = lease
	return nil, bytes, err
}

func transitionStoredBlock(ctx context.Context, tx *sql.Tx, mutation workboard.ControlMutation, card *workboard.Card, body *storedWorkboardCard) (int64, int, error) {
	if card.Revision != mutation.ExpectedCardRevision {
		return 0, 0, &workboard.Violation{Code: workboard.CodeStaleRevision, Field: "card_revision"}
	}
	if card.CurrentAttemptID != mutation.AttemptID || card.CurrentClaimID != mutation.ClaimID ||
		mutation.Kind == workboard.ControlBlock && card.State != workboard.InProgress ||
		mutation.Kind == workboard.ControlUnblock && (card.State != workboard.Blocked || card.BlockReason != mutation.ReasonCode) {
		return 0, 0, &workboard.Violation{Code: workboard.CodeIllegalTransition, Field: "state"}
	}
	lease, _, err := readLifecycleClaim(ctx, tx, mutation.BoardID, mutation.CardID, mutation.AttemptID, mutation.ClaimID)
	if err != nil {
		return 0, 0, err
	}
	if lease.Revision != mutation.ExpectedClaimRevision {
		return 0, 0, &workboard.Violation{Code: workboard.CodeStaleRevision, Field: "claim_revision"}
	}
	if lease.OwnerID != mutation.Actor.ID {
		return 0, 0, &workboard.Violation{Code: workboard.CodeLeaseOwner, Field: "owner"}
	}
	if lease.State != workboard.LeaseActive || !mutation.Now.Before(lease.ExpiresAt) {
		return 0, 0, &workboard.Violation{Code: workboard.CodeLeaseExpired, Field: "claim"}
	}
	if mutation.Kind == workboard.ControlBlock {
		card.State, card.BlockReason = workboard.Blocked, mutation.ReasonCode
	} else {
		card.State, card.BlockReason = workboard.InProgress, ""
	}
	card.Revision++
	card.UpdatedAt = mutation.Now
	bytes, err := writeLifecycleCard(ctx, tx, *card, *body, mutation.ExpectedCardRevision)
	return lease.Revision, bytes, err
}
