package remote

import (
	"context"
	"time"

	"github.com/ArronJablonowski/NexusRouter/harness"
)

// OutcomeReview names the exact destination/caller/request/event receipt as well
// as the immutable execution reviewed. A matching execution digest on another
// system is not permission to transfer that system's evaluation here.
type OutcomeReview struct {
	Version       int            `json:"version"`
	ReceiptSHA256 string         `json:"receipt_sha256"`
	Review        harness.Review `json:"review"`
}

// Digest is a content binding, not authentication. Imported receipts are never
// accepted as canonical by RecordedOutcome or ReviewRecordedOutcome.
func (r OutcomeReceipt) Digest() (string, error) {
	if r.Version != Version || !r.Route.valid() || !name(r.SubmissionID) || !hexDigest(r.EventsSHA256) || r.Execution.Validate() != nil || r.Execution.Status != "completed" {
		return "", ErrInvalid
	}
	return hash(r), nil
}

// ReviewRecordedOutcome is an operator/evaluator-only local write, never a
// model tool or remote HTTP operation. The embedding host must authenticate the
// reviewer and evaluation method before calling; reviewer labels are not proof
// of identity. The reviewer must have evaluated the exact bound output.
//
// Fresh authenticated reads revalidate the saved destination and actual journal.
// No inference or evaluation is performed here. Revisions require ExpectedHead;
// identical retries do not add learning weight. AI reviews retain the ledger's
// advisory classification instead of masquerading as deterministic/human votes.
func (c *Client) ReviewRecordedOutcome(ctx context.Context, routes *RouteStore, root, key string, task Task, evaluation OutcomeReview) error {
	if evaluation.Version != Version || !hexDigest(evaluation.ReceiptSHA256) || evaluation.Review.Validate() != nil {
		return ErrInvalid
	}
	now := time.Now().UTC()
	if evaluation.Review.CreatedAt.After(now) {
		return ErrInvalid
	}
	verified, err := c.RecordedOutcome(ctx, routes, key, task)
	if err != nil {
		return err
	}
	receiptDigest, err := verified.receipt.Digest()
	if err != nil {
		return err
	}
	executionDigest, err := verified.receipt.Execution.Digest()
	if err != nil || receiptDigest != evaluation.ReceiptSHA256 || executionDigest != evaluation.Review.ExecutionDigest || evaluation.Review.CreatedAt.Before(verified.receipt.Execution.CompletedAt) {
		return ErrConflict
	}
	// All input bindings are checked before any local evidence is written.
	ledger, err := verified.recordLedger(ctx, root, now)
	if err != nil {
		return err
	}
	defer ledger.Close()
	return ledger.AppendReview(ctx, evaluation.Review, time.Now().UTC())
}
