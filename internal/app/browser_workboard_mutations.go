package app

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/ArronJablonowski/DarwinRouter/internal/browserops"
	contract "github.com/ArronJablonowski/DarwinRouter/webui"
	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

// BrowserWorkboardMutations adds session-scoped reconciliation around the
// workboard's own transactional idempotency receipts. Browser journal rows
// remain bound to the ephemeral session subject, while the trusted bridge uses
// one stable local-workspace authority. After a daemon restart, a new browser
// session can therefore submit the same key and receive the exact durable
// receipt without weakening process-local session revocation.
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
	if receipt, recovered, err := b.recoverPending(ctx, subject, request); err != nil || recovered {
		return receipt, err
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

func (b *BrowserWorkboardMutations) recoverPending(ctx context.Context, recoverySubject string, request contract.BoardRequest) (contract.OperationReceipt, bool, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return contract.OperationReceipt{}, false, ErrAdmission
	}
	record, found, err := b.journal.store.Adoptable(ctx, recoverySubject, request.IdempotencyKey, string(request.Action), body)
	if err != nil || !found {
		return contract.OperationReceipt{}, false, err
	}
	var receipt contract.OperationReceipt
	var mutationErr error
	if record.Legacy {
		receipt, mutationErr = b.bridge.legacyBrowserMutate(ctx, record.Subject, request)
	} else {
		receipt, mutationErr = b.bridge.BrowserMutate(ctx, recoverySubject, request)
	}
	if mutationErr != nil {
		code, definitive := browserRejectionCode(mutationErr)
		if !definitive {
			return contract.OperationReceipt{}, true, mutationErr
		}
		subjectType, subjectID := workboardRequestSubject(request)
		field := ""
		var violation *workboard.Violation
		if errors.As(mutationErr, &violation) {
			field = violation.Field
		}
		rejected, marshalErr := json.Marshal(browserRejection{Version: 1, Code: code, SubjectType: subjectType, SubjectID: subjectID, Field: field})
		if marshalErr != nil {
			return contract.OperationReceipt{}, true, ErrBrowserMutation
		}
		if _, err = b.journal.store.Recover(ctx, recoverySubject, record, "rejected", rejected); err != nil {
			return contract.OperationReceipt{}, true, ErrBrowserMutation
		}
		return contract.OperationReceipt{}, true, &BrowserOperationError{OperationID: record.OperationID, Cause: mutationErr}
	}
	if receipt.Validate() != nil || !workboardReceiptMatchesRequest(receipt, request) {
		return contract.OperationReceipt{}, true, ErrBrowserMutation
	}
	response, err := json.Marshal(receipt)
	if err != nil {
		return contract.OperationReceipt{}, true, ErrBrowserMutation
	}
	if _, err = b.journal.store.Recover(ctx, recoverySubject, record, "committed", response); err != nil {
		return contract.OperationReceipt{}, true, ErrBrowserMutation
	}
	return receipt, true, nil
}

func workboardReceiptMatchesRequest(receipt contract.OperationReceipt, request contract.BoardRequest) bool {
	if receipt.Validate() != nil {
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
