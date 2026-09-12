package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

func (s *Store) ApplyProgressMutation(ctx context.Context, mutation workboard.ProgressMutation) (workboard.OperationReceipt, error) {
	if err := validateProgressMutation(mutation); err != nil {
		return workboard.OperationReceipt{}, err
	}
	requestDigest, err := workboard.ProgressDigest(mutation)
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
	card, cardBody, err := readStoredCard(ctx, tx, mutation.BoardID, mutation.CardID)
	if err != nil {
		return workboard.OperationReceipt{}, err
	}
	var claimRevision *int64
	var durableBytes int
	switch mutation.Kind {
	case workboard.ProgressCriteriaRevise:
		durableBytes, err = reviseStoredCriteria(ctx, tx, mutation, &card, &cardBody)
	case workboard.ProgressCheckpointAppend:
		var revision int64
		revision, durableBytes, err = appendStoredCheckpoint(ctx, tx, mutation, card, cardBody)
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
		Kind: progressAction(mutation.Kind), ActorID: mutation.Actor.ID, ActorType: mutation.Actor.Type, CardID: card.ID, CreatedAt: mutation.Now}
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
	result, err := tx.ExecContext(ctx, `UPDATE workboard_boards SET revision=?,event_sequence=?,updated_at=?,body=? WHERE id=? AND revision=?`,
		board.Revision, board.EventSequence, board.UpdatedAt.UnixNano(), boardBody, board.ID, board.Revision-1)
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

func reviseStoredCriteria(ctx context.Context, tx *sql.Tx, mutation workboard.ProgressMutation, card *workboard.Card, body *storedWorkboardCard) (int, error) {
	if card.Revision != mutation.ExpectedCardRevision {
		return 0, &workboard.Violation{Code: workboard.CodeStaleRevision, Field: "card_revision"}
	}
	if card.CriteriaRevision != mutation.ExpectedCriteriaRevision {
		return 0, &workboard.Violation{Code: workboard.CodeStaleRevision, Field: "criteria_revision"}
	}
	if card.State != workboard.Backlog && card.State != workboard.Ready || card.CurrentClaimID != "" {
		return 0, &workboard.Violation{Code: workboard.CodeIllegalTransition, Field: "criteria"}
	}
	newDigest := criteriaDigest(mutation.Criteria)
	oldDigest := criteriaDigest(card.Criteria)
	if newDigest == "" || newDigest == oldDigest {
		return 0, &workboard.Violation{Code: workboard.CodeInvalid, Field: "criteria"}
	}
	card.CriteriaRevision++
	card.Criteria = append([]workboard.AcceptanceCriterion{}, mutation.Criteria...)
	card.Revision++
	card.UpdatedAt = mutation.Now
	body.CriteriaRevision = card.CriteriaRevision
	body.Criteria = storedCardCriteria(card.Criteria)
	*body = updateStoredBody(*body, *card)
	bodyBytes, err := json.Marshal(*body)
	if err != nil || card.Validate() != nil || !validStoredCardReferences(*body) {
		return 0, ErrWorkboardCorrupt
	}
	result, err := tx.ExecContext(ctx, `UPDATE workboard_cards SET revision=?,criteria_revision=?,updated_at=?,body=? WHERE board_id=? AND id=? AND revision=? AND criteria_revision=?`,
		card.Revision, card.CriteriaRevision, card.UpdatedAt.UnixNano(), bodyBytes, card.BoardID, card.ID, mutation.ExpectedCardRevision, mutation.ExpectedCriteriaRevision)
	if err != nil {
		return 0, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return 0, &workboard.Violation{Code: workboard.CodeStaleRevision, Field: "card_revision"}
	}
	total := len(bodyBytes)
	for ordinal, criterion := range body.Criteria {
		criterionBody, marshalErr := json.Marshal(criterion)
		if marshalErr != nil {
			return 0, marshalErr
		}
		total += len(criterionBody)
		if _, err = tx.ExecContext(ctx, `INSERT INTO workboard_criteria(board_id,card_id,criteria_revision,ordinal,id,kind,required_source,validator_id,description,required,body)
			VALUES(?,?,?,?,?,?,?,?,?,?,?)`, card.BoardID, card.ID, card.CriteriaRevision, ordinal, criterion.ID, criterion.Kind,
			criterion.RequiredSource, criterion.ValidatorID, criterion.Description, boolInt(criterion.Required), criterionBody); err != nil {
			return 0, normalizeProgressWriteError(err)
		}
	}
	return total, nil
}

func criteriaDigest(criteria []workboard.AcceptanceCriterion) string {
	return workboard.AcceptanceCriteriaDigest(criteria)
}
