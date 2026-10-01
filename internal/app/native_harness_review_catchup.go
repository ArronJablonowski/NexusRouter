package app

import (
	"context"
	"errors"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
)

// HarnessAuditPage reports copies and preserved existing heads. Cursor resets at
// cycle end so reviews completed after an earlier scan are eventually discovered.
// Copies include exact idempotent replays; they are not new learning samples.
type HarnessAuditPage struct {
	Cursor        HarnessEvidenceCursor
	Copied        int
	Conflicts     int
	CycleComplete bool
}

func ReconcileHarnessAuditPage(ctx context.Context, path string, ledger *harness.EvidenceStore, cursor HarnessEvidenceCursor) (HarnessAuditPage, error) {
	out := HarnessAuditPage{Cursor: cursor}
	if ctx == nil || ledger == nil || cursor.After < 0 || (cursor.Workspace == "" && cursor.After != 0) {
		return out, ErrAdmission
	}
	db, err := telemetry.OpenReadOnly(ctx, path)
	if err != nil {
		return out, err
	}
	defer db.Close()
	return reconcileHarnessAuditPageStore(ctx, db, ledger, cursor)
}

func reconcileHarnessAuditPageStore(ctx context.Context, db *telemetry.Store, ledger *harness.EvidenceStore, cursor HarnessEvidenceCursor) (HarnessAuditPage, error) {
	out := HarnessAuditPage{Cursor: cursor}
	workspace, err := db.WorkspaceIdentity(ctx)
	if err != nil {
		return out, err
	}
	if cursor.Workspace != "" && cursor.Workspace != workspace {
		return out, ErrAdmission
	}
	out.Cursor.Workspace = workspace
	rows, err := db.ScanHarnessReviews(ctx, cursor.After, 100)
	if err != nil {
		return out, err
	}
	for _, row := range rows {
		if row.Operation != "" {
			_, err = reconcileHarnessAuditStore(ctx, db, ledger, row.TaskID, row.Operation)
			if errors.Is(err, errHarnessReviewHeadConflict) {
				out.Conflicts++
			} else if err != nil {
				return out, err
			} else {
				out.Copied++
			}
		}
		out.Cursor.After = row.Position
	}
	if len(rows) < 100 {
		out.Cursor.After = 0
		out.CycleComplete = true
	}
	return out, nil
}
