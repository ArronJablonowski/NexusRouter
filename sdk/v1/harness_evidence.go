package v1

import (
	"context"
	"time"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

// ReconcileHarnessOutcome copies a verified completed outcome from this client's
// canonical task journal into the host-owned evidence ledger. It never executes
// inference or creates a quality vote. Retry this operation, not Run, after an
// uncertain ledger write. The host owns the ledger's lifetime and access policy.
func (c *Client) ReconcileHarnessOutcome(ctx context.Context, ledger *harness.EvidenceStore, task string) (harness.Execution, error) {
	if !c.valid(ctx) || ledger == nil {
		return harness.Execution{}, ErrAdmission
	}
	return runtime.RecordHarnessOutcome(ctx, sdkHarnessReader{c}, ledger, task, time.Now().UTC())
}

type sdkHarnessReader struct{ client *Client }

func (r sdkHarnessReader) Read(ctx context.Context, task string, after int64, limit int) ([]runtime.Event, error) {
	page, err := r.client.ReadEvents(ctx, task, after, limit)
	if err != nil {
		return nil, err
	}
	return page.Events, nil
}

// ReviewHarnessOutcome is an operator/evaluator-only write, never a model tool.
// The embedding host must authenticate the reviewer and evaluation method before
// calling it; labels in Review do not authenticate their author. ExecutionDigest
// must identify the exact canonical completed output that was actually reviewed.
// Revisions require ExpectedHead; identical retries do not add learning weight.
// This does not synthesize judgments, rerun inference or modify the task journal.
func (c *Client) ReviewHarnessOutcome(ctx context.Context, ledger *harness.EvidenceStore, task string, review harness.Review) error {
	if !c.valid(ctx) || ledger == nil || review.Validate() != nil {
		return ErrAdmission
	}
	now := time.Now().UTC()
	if review.CreatedAt.After(now) {
		return harness.ErrInvalid
	}
	// Read and bind before writing anything: a review for another execution must
	// not even reconcile the requested task as a side effect.
	events, err := (sdkHarnessReader{c}).Read(ctx, task, 0, 3)
	if err != nil {
		return err
	}
	if len(events) != 2 || events[1].Data.HarnessOutcome == nil {
		return runtime.ErrProtocol
	}
	outcome := *events[1].Data.HarnessOutcome
	digest, err := outcome.Digest()
	if err != nil || digest != review.ExecutionDigest || review.CreatedAt.Before(outcome.CompletedAt) {
		return harness.ErrConflict
	}
	// Reconciliation rechecks the complete start/terminal protocol and immutable
	// output binding using the same canonical reader. No caller outcome is trusted.
	if _, err = c.ReconcileHarnessOutcome(ctx, ledger, task); err != nil {
		return err
	}
	return ledger.AppendReview(ctx, review, time.Now().UTC())
}
