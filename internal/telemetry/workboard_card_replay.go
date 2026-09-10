package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

type cardMutationResponse struct {
	Version int                        `json:"version"`
	Receipt workboard.OperationReceipt `json:"receipt"`
	Card    workboard.Card             `json:"card"`
}

// ReplayCardMutation performs a read-only exact-retry probe used by
// CardService before current-state validation. It never reserves a key.
func (s *Store) ReplayCardMutation(ctx context.Context, mutation workboard.CardMutation) (workboard.Card, bool, error) {
	if err := validateStoreMutation(mutation); err != nil {
		return workboard.Card{}, false, err
	}
	want, err := cardMutationDigest(mutation)
	if err != nil || want != mutation.RequestDigest {
		return workboard.Card{}, false, invalidWorkboard("request_digest")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return workboard.Card{}, false, err
	}
	defer tx.Rollback()
	card, found, err := readCardMutationReplay(ctx, tx, mutation.BoardID, digestBytes([]byte(mutation.IdempotencyKey)), want)
	if err != nil || !found {
		return workboard.Card{}, found, err
	}
	if err = tx.Commit(); err != nil {
		return workboard.Card{}, false, err
	}
	return card, true, nil
}

func finalizeCardMutationReceipt(receipt *workboard.OperationReceipt, card workboard.Card, durableBytes int) ([]byte, error) {
	if durableBytes < 1 || durableBytes > workboard.MaxTransactionBytes {
		return nil, &workboard.Violation{Code: workboard.CodeLimitExceeded, Field: "transaction_bytes"}
	}
	for range 8 {
		receipt.ResponseDigest = receiptDigest(*receipt)
		response, err := json.Marshal(cardMutationResponse{Version: 1, Receipt: *receipt, Card: card})
		if err != nil {
			return nil, err
		}
		total := durableBytes + len(response)
		if total > workboard.MaxTransactionBytes {
			return nil, &workboard.Violation{Code: workboard.CodeLimitExceeded, Field: "transaction_bytes"}
		}
		if receipt.TransactionBytes == total {
			if receipt.Validate() != nil || card.Validate() != nil {
				return nil, ErrWorkboardCorrupt
			}
			return response, nil
		}
		receipt.TransactionBytes = total
	}
	return nil, ErrWorkboardCorrupt
}

func readCardMutationReplay(ctx context.Context, tx *sql.Tx, boardID, keyDigest, requestDigest string) (workboard.Card, bool, error) {
	var operationID, storedBoardID, storedRequestDigest, responseDigest, outcome string
	var firstSequence, lastSequence int64
	var eventCount, transactionBytes int
	var createdAt int64
	var response []byte
	err := tx.QueryRowContext(ctx, `SELECT operation_id,board_id,request_digest,response_digest,first_sequence,last_sequence,event_count,transaction_bytes,outcome,response,created_at
		FROM workboard_operations WHERE scope_kind='board' AND scope_id=? AND key_digest=?`, boardID, keyDigest).
		Scan(&operationID, &storedBoardID, &storedRequestDigest, &responseDigest, &firstSequence, &lastSequence, &eventCount, &transactionBytes, &outcome, &response, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return workboard.Card{}, false, nil
	}
	if err != nil {
		return workboard.Card{}, false, err
	}
	if storedRequestDigest != requestDigest {
		return workboard.Card{}, true, ErrConflict
	}
	var envelope cardMutationResponse
	if strictJSON(response, &envelope) != nil {
		return workboard.Card{}, true, ErrWorkboardCorrupt
	}
	indexed := workboard.OperationReceipt{Version: 1, BoardID: storedBoardID, OperationID: operationID, RequestDigest: storedRequestDigest,
		ResponseDigest: responseDigest, FirstSequence: firstSequence, LastSequence: lastSequence, EventCount: eventCount, TransactionBytes: transactionBytes,
		BoardRevision: envelope.Receipt.BoardRevision, CardID: envelope.Receipt.CardID, CardRevision: envelope.Receipt.CardRevision,
		ClaimRevision: envelope.Receipt.ClaimRevision, Outcome: outcome, CreatedAt: time.Unix(0, createdAt).UTC()}
	if envelope.Version != 1 || envelope.Receipt != indexed || envelope.Receipt.Validate() != nil || envelope.Receipt.ResponseDigest != receiptDigest(envelope.Receipt) ||
		envelope.Card.Validate() != nil || envelope.Card.BoardID != boardID || envelope.Card.ID != envelope.Receipt.CardID || envelope.Receipt.CardRevision == nil ||
		envelope.Card.Revision != *envelope.Receipt.CardRevision {
		return workboard.Card{}, true, ErrWorkboardCorrupt
	}
	if err = verifyWorkboardReceiptEvents(ctx, tx, envelope.Receipt); err != nil {
		return workboard.Card{}, true, err
	}
	return envelope.Card, true, nil
}
