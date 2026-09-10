package app

import (
	"context"

	"github.com/ArronJablonowski/DarwinRouter/internal/browserops"
	contract "github.com/ArronJablonowski/DarwinRouter/webui"
)

// BrowserWorkboardMutations adds session-scoped reconciliation around the
// workboard's own transactional idempotency receipts. A pending journal row is
// safe to resume: the bridge reuses the same workboard idempotency key and
// actor, so the durable command store either applies it once or returns its
// exact committed receipt.
type BrowserWorkboardMutations struct {
	bridge  *WorkboardBridge
	journal *BrowserMutations
}

func NewBrowserWorkboardMutations(bridge *WorkboardBridge, store *browserops.Store) (*BrowserWorkboardMutations, error) {
	if bridge == nil || store == nil {
		return nil, ErrAdmission
	}
	return &BrowserWorkboardMutations{bridge: bridge, journal: &BrowserMutations{store: store}}, nil
}

func (b *BrowserWorkboardMutations) Mutate(ctx context.Context, subject string, request contract.BoardRequest) (contract.OperationReceipt, error) {
	if b == nil || b.bridge == nil || b.journal == nil || ctx == nil || ctx.Err() != nil ||
		!validBrowserSubject(subject) || request.Validate() != nil {
		return contract.OperationReceipt{}, ErrAdmission
	}
	record, replay, err := b.journal.begin(ctx, subject, string(request.Action), request.IdempotencyKey, request)
	if err != nil {
		return contract.OperationReceipt{}, err
	}
	if replay {
		var receipt contract.OperationReceipt
		if decodeReceipt(record.Response, &receipt) != nil || !workboardReceiptMatchesRequest(receipt, request) {
			return contract.OperationReceipt{}, ErrBrowserMutation
		}
		return receipt, nil
	}
	receipt, err := b.bridge.BrowserMutate(ctx, subject, request)
	if err != nil {
		subjectType, subjectID := workboardRequestSubject(request)
		return contract.OperationReceipt{}, b.journal.reject(ctx, subject, record, subjectType, subjectID, err)
	}
	if receipt.Validate() != nil || !workboardReceiptMatchesRequest(receipt, request) {
		return contract.OperationReceipt{}, ErrBrowserMutation
	}
	if err = b.journal.commit(ctx, subject, record, receipt); err != nil {
		return contract.OperationReceipt{}, err
	}
	return receipt, nil
}

func workboardReceiptMatchesRequest(receipt contract.OperationReceipt, request contract.BoardRequest) bool {
	if receipt.Validate() != nil || receipt.OperationID != request.IdempotencyKey {
		return false
	}
	if request.BoardID != "" && receipt.BoardID != request.BoardID || request.CardID != "" && receipt.CardID != request.CardID {
		return false
	}
	if request.Action == contract.BoardCreate || request.Action == contract.BoardRevise || request.Action == contract.BoardArchive {
		return receipt.CardID == ""
	}
	return receipt.CardID != ""
}

func workboardRequestSubject(request contract.BoardRequest) (string, string) {
	if request.CardID != "" {
		return "card", request.CardID
	}
	if request.BoardID != "" {
		return "board", request.BoardID
	}
	return "", ""
}
