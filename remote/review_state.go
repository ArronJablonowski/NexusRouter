package remote

import (
	"context"
	"github.com/ArronJablonowski/NexusRouter/harness"
	"path/filepath"
	"time"
)

type AutomaticReviewState struct {
	Version         int
	ReceiptSHA256   string
	ExecutionSHA256 string
	Recorded        bool
	Evidence        *harness.ReviewState
}

// InspectAutomaticReview authenticates the original route and completion, then
// reads only its caller/destination evidence. Missing evidence remains unrecorded.
// This never creates a ledger, reviews content or dispatches an evaluator.
func (c *Client) InspectAutomaticReview(ctx context.Context, routes *RouteStore, root, key string, request AutomaticRequest) (AutomaticReviewState, error) {
	var out AutomaticReviewState
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return out, ErrInvalid
	}
	verified, err := c.AutomaticOutcome(ctx, routes, key, request)
	if err != nil {
		return out, err
	}
	receipt := verified.Receipt()
	receiptDigest, err := receipt.Digest()
	if err != nil {
		return out, err
	}
	executionDigest, err := receipt.Execution.Digest()
	if err != nil {
		return out, err
	}
	snapshot, err := readDestinationEvidence(ctx, root, receipt.Route.Destination, receipt.Route.CallerFingerprint, time.Now().UTC())
	if err != nil {
		return out, err
	}
	state, found, err := snapshot.ReviewState(executionDigest)
	if err != nil {
		return out, err
	}
	out = AutomaticReviewState{Version: Version, ReceiptSHA256: receiptDigest, ExecutionSHA256: executionDigest, Recorded: found}
	if found {
		out.Evidence = &state
	}
	return out, nil
}
