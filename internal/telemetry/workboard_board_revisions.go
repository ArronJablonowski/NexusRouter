package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

// ReviseWorkboard updates active board metadata atomically with its immutable
// event and replay receipt. Exact replay is resolved before revision/state
// checks so a committed retry remains successful after its fence becomes old.
func (s *Store) ReviseWorkboard(ctx context.Context, request workboard.ReviseBoardRequest, actor workboard.Actor, now time.Time) (workboard.OperationReceipt, error) {
	if request.Validate() != nil || actor.Validate() != nil {
		return workboard.OperationReceipt{}, invalidWorkboard("request")
	}
	now = now.UTC()
	if now.Year() < 1970 || now.Year() >= 2261 {
		return workboard.OperationReceipt{}, invalidWorkboard("created_at")
	}
	requestDigest, err := digestJSON(struct {
		Version          int             `json:"version"`
		Action           string          `json:"action"`
		BoardID          string          `json:"board_id"`
		ExpectedRevision int64           `json:"expected_board_revision"`
		Title            *string         `json:"title"`
		Description      *string         `json:"description"`
		Actor            workboard.Actor `json:"actor"`
	}{request.Version, string(workboard.BoardReviseAction), request.BoardID, request.ExpectedRevision, request.Title, request.Description, actor})
	if err != nil {
		return workboard.OperationReceipt{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return workboard.OperationReceipt{}, err
	}
	defer tx.Rollback()
	if err = reserveWorkboardWriter(ctx, tx); err != nil {
		return workboard.OperationReceipt{}, err
	}
	keyDigest := digestBytes([]byte(request.IdempotencyKey))
	if receipt, found, replayErr := readWorkboardReceipt(ctx, tx, "board", request.BoardID, keyDigest, requestDigest); found || replayErr != nil {
		return receipt, replayErr
	}
	board, _, _, err := readBoardRow(ctx, tx, request.BoardID)
	if err != nil {
		return workboard.OperationReceipt{}, err
	}
	if board.Revision != request.ExpectedRevision {
		return workboard.OperationReceipt{}, &workboard.Violation{Code: workboard.CodeStaleRevision, Field: "board_revision"}
	}
	if board.State != "active" {
		return workboard.OperationReceipt{}, &workboard.Violation{Code: workboard.CodeIllegalTransition, Field: "board_state"}
	}
	previousTitle, previousDescription := board.Title, board.Description
	if request.Title != nil {
		board.Title = *request.Title
	}
	if request.Description != nil {
		board.Description = *request.Description
	}
	if board.Title == previousTitle && board.Description == previousDescription {
		return workboard.OperationReceipt{}, invalidWorkboard("patch")
	}
	operationID, eventID := newWorkboardID(), newWorkboardID()
	if operationID == "" || eventID == "" {
		return workboard.OperationReceipt{}, errors.New("secure identifier generation failed")
	}
	board.Revision++
	board.EventSequence++
	board.UpdatedAt = now
	if board.Validate() != nil {
		return workboard.OperationReceipt{}, ErrWorkboardCorrupt
	}
	boardBody, err := json.Marshal(board)
	if err != nil {
		return workboard.OperationReceipt{}, err
	}
	event := workboard.BoardEvent{Version: 1, ID: eventID, BoardID: board.ID, Sequence: board.EventSequence, OperationID: operationID,
		Kind: workboard.BoardReviseAction, ActorID: actor.ID, ActorType: actor.Type, CreatedAt: now}
	eventBody, err := json.Marshal(event)
	if err != nil || event.Validate() != nil || len(eventBody) == 0 || len(eventBody) > workboard.MaxTransactionBytes {
		return workboard.OperationReceipt{}, invalidWorkboard("event")
	}
	receipt := workboard.OperationReceipt{Version: 1, BoardID: board.ID, OperationID: operationID, RequestDigest: requestDigest,
		FirstSequence: board.EventSequence, LastSequence: board.EventSequence, EventCount: 1, BoardRevision: board.Revision,
		Outcome: "committed", CreatedAt: now}
	response, err := finalizeWorkboardReceipt(&receipt, len(boardBody)+len(eventBody))
	if err != nil {
		return workboard.OperationReceipt{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE workboard_boards SET revision=?,event_sequence=?,title=?,description=?,updated_at=?,body=?
		WHERE id=? AND revision=? AND state='active'`, board.Revision, board.EventSequence, board.Title, board.Description, now.UnixNano(), boardBody, board.ID, request.ExpectedRevision)
	if err != nil {
		return workboard.OperationReceipt{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return workboard.OperationReceipt{}, &workboard.Violation{Code: workboard.CodeStaleRevision, Field: "board_revision"}
	}
	if err = insertWorkboardOperation(ctx, tx, "board", board.ID, keyDigest, receipt, response); err != nil {
		return workboard.OperationReceipt{}, err
	}
	if err = insertWorkboardEvent(ctx, tx, eventID, board.ID, board.EventSequence, operationID, string(workboard.BoardReviseAction), "", actor, now, eventBody); err != nil {
		return workboard.OperationReceipt{}, err
	}
	if err = tx.Commit(); err != nil {
		return workboard.OperationReceipt{}, err
	}
	return receipt, nil
}
