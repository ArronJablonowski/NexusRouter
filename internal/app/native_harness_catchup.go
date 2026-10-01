package app

import (
	"context"
	"sync"
	"time"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/health"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

// HarnessEvidenceCursor is host-owned scan state, bound to one canonical journal.
// Start at zero after restart or replacing an evidence ledger. Replaying copies
// is idempotent and never creates reviews. Do not accept cursors from model output.
type HarnessEvidenceCursor struct {
	Workspace string
	After     int64
}

// ReconcileHarnessEvidencePage inspects at most 100 events. On failure the cursor
// stops before the failed copy, allowing retry without silently dropping evidence.
func ReconcileHarnessEvidencePage(ctx context.Context, path string, ledger *harness.EvidenceStore, cursor HarnessEvidenceCursor) (HarnessEvidenceCursor, int, error) {
	if ctx == nil || ledger == nil || cursor.After < 0 || (cursor.Workspace == "" && cursor.After != 0) {
		return cursor, 0, ErrAdmission
	}
	db, err := telemetry.OpenReadOnly(ctx, path)
	if err != nil {
		return cursor, 0, err
	}
	defer db.Close()
	return reconcileHarnessEvidenceStore(ctx, db, ledger, cursor)
}

func reconcileHarnessEvidenceStore(ctx context.Context, db *telemetry.Store, ledger *harness.EvidenceStore, cursor HarnessEvidenceCursor) (HarnessEvidenceCursor, int, error) {
	workspace, err := db.WorkspaceIdentity(ctx)
	if err != nil {
		return cursor, 0, err
	}
	if cursor.Workspace != "" && cursor.Workspace != workspace {
		return cursor, 0, ErrAdmission
	}
	cursor.Workspace = workspace
	rows, err := db.ScanHarnessCompletions(ctx, cursor.After, 100)
	if err != nil {
		return cursor, 0, err
	}
	copied := 0
	for _, row := range rows {
		if row.TaskID != "" {
			if _, err = runtime.RecordHarnessOutcome(ctx, db, ledger, row.TaskID, time.Now().UTC()); err != nil {
				return cursor, copied, err
			}
			copied++
		}
		cursor.After = row.Position
	}
	return cursor, copied, nil
}

// HarnessEvidenceCatchup owns only its worker, never the caller's ledger.
// Close cancels and joins the worker before the host closes its ledger.
type HarnessEvidenceCatchup struct {
	cancel context.CancelFunc
	done   chan struct{}
	mu     sync.Mutex
	check  health.Check
}

func (w *HarnessEvidenceCatchup) Health() health.Check {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.check
}
func (w *HarnessEvidenceCatchup) Close() { w.cancel(); <-w.done }
func (w *HarnessEvidenceCatchup) set(status, code string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.check = health.Check{Component: "harness_evidence", Status: status, Code: code}
}

func StartHarnessEvidenceCatchup(ctx context.Context, s *Service, db *telemetry.Store) (*HarnessEvidenceCatchup, error) {
	if ctx == nil || ctx.Err() != nil || s == nil || db == nil {
		return nil, ErrAdmission
	}
	child, cancel := context.WithCancel(ctx)
	w := &HarnessEvidenceCatchup{cancel: cancel, done: make(chan struct{})}
	if s.harnessEvidence == nil {
		w.set("disabled", "disabled_by_policy")
		close(w.done)
		return w, nil
	}
	w.set("unknown", "supervisor_starting")
	ledger := s.harnessEvidence
	go func() {
		defer close(w.done)
		defer w.set("unavailable", "supervisor_stopped")
		var cursor HarnessEvidenceCursor
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			if child.Err() != nil {
				return
			}
			page, cancelPage := context.WithTimeout(child, 5*time.Second)
			next, _, err := reconcileHarnessEvidenceStore(page, db, ledger, cursor)
			cancelPage()
			cursor = next
			if err != nil {
				w.set("degraded", "supervisor_error")
			} else {
				w.set("healthy", "supervisor_ok")
			}
			select {
			case <-child.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return w, nil
}
