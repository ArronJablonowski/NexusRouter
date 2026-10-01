package v1

import (
	"context"
	"time"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/internal/app"
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

// ReconcileHarnessAudit binds a completed durable RunAudit operation to the
// joint ledger as advisory AI evidence. It performs no evaluator/inference call,
// never overwrites an existing different review head, and is safe to retry.
func (c *Client) ReconcileHarnessAudit(ctx context.Context, ledger *harness.EvidenceStore, task, operation string) (harness.Review, error) {
	if !c.valid(ctx) {
		return harness.Review{}, ErrAdmission
	}
	return app.ReconcileHarnessAudit(ctx, c.database, ledger, task, operation)
}

// HarnessEvidenceCursor identifies progress in one canonical journal.
type HarnessEvidenceCursor = app.HarnessEvidenceCursor

// ReconcileHarnessEvidencePage repairs pending outcome copies in a bounded batch.
// Hosts schedule pages and own ledger lifetime; no inference or review is run.
func (c *Client) ReconcileHarnessEvidencePage(ctx context.Context, ledger *harness.EvidenceStore, cursor HarnessEvidenceCursor) (HarnessEvidenceCursor, int, error) {
	if !c.valid(ctx) {
		return cursor, 0, ErrAdmission
	}
	return app.ReconcileHarnessEvidencePage(ctx, c.database, ledger, cursor)
}

// HarnessAuditPage reports bounded review recovery progress and preserved heads.
type HarnessAuditPage = app.HarnessAuditPage

// ReconcileHarnessAuditPage copies completed canonical advisory reviews without
// invoking an evaluator. Existing different heads remain authoritative. Schedule
// repeated pages, including after CycleComplete, to discover later completions.
func (c *Client) ReconcileHarnessAuditPage(ctx context.Context, ledger *harness.EvidenceStore, cursor HarnessEvidenceCursor) (HarnessAuditPage, error) {
	if !c.valid(ctx) {
		return HarnessAuditPage{Cursor: cursor}, ErrAdmission
	}
	return app.ReconcileHarnessAuditPage(ctx, c.database, ledger, cursor)
}
