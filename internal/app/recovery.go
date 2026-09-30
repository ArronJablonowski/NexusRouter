package app

import (
	"context"
	"errors"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

func (d *Dispatcher) reconcile(ctx context.Context, configDigest string) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	after := ""
	readerAfter := ""
	workerAfter := ""
	attentionAfter := ""
	configAfter := ""
	workboardAfter := ""
	compactionAfter := d.compactionRecoveryAfter
	summaryAfter := d.summaryRecoveryAfter
	for ctx.Err() == nil {
		d.supervisorHeartbeat(-1)
		query, cancel := context.WithTimeout(ctx, 5*time.Second)
		nextCompaction, _, compactionErr := d.db.ReconcileContextCompactionPlansPage(query, compactionAfter, 32, time.Now().UTC())
		cancel()
		compactionAfter = nextCompaction
		d.supervisorHeartbeat(-1)
		if compactionErr != nil && !transientSupervisorError(compactionErr) && ctx.Err() == nil {
			d.recordError()
		}
		if ctx.Err() != nil {
			return
		}
		query, cancel = context.WithTimeout(ctx, 5*time.Second)
		nextSummary, _, summaryErr := d.db.ReconcileSummaryAttemptsPage(query, summaryAfter, 32, time.Now().UTC())
		cancel()
		summaryAfter = nextSummary
		d.supervisorHeartbeat(-1)
		if summaryErr != nil && !transientSupervisorError(summaryErr) && ctx.Err() == nil {
			d.recordError()
		}
		if ctx.Err() != nil {
			return
		}
		query, cancel = context.WithTimeout(ctx, 5*time.Second)
		nextWorker, _, workerErr := d.db.RecoverOrphanWorkersPage(query, workerAfter, 32, time.Now().UTC())
		cancel()
		workerAfter = nextWorker
		d.supervisorHeartbeat(-1)
		if workerErr != nil && !transientSupervisorError(workerErr) && ctx.Err() == nil {
			d.recordError()
		}
		if ctx.Err() != nil {
			return
		}
		query, cancel = context.WithTimeout(ctx, 5*time.Second)
		nextConfig, configErr := d.reconcileConfigurationPage(query, configDigest, configAfter)
		cancel()
		d.supervisorHeartbeat(-1)
		if nextConfig != "" {
			configAfter = nextConfig
		}
		if configErr != nil && !transientSupervisorError(configErr) {
			if ctx.Err() == nil {
				d.recordError()
			}
		}
		if ctx.Err() != nil {
			return
		}
		query, cancel = context.WithTimeout(ctx, 5*time.Second)
		next, err := d.recoverPage(query, configDigest, after)
		cancel()
		d.supervisorHeartbeat(-1)
		if err != nil && !transientSupervisorError(err) {
			if ctx.Err() == nil {
				d.recordError()
			}
		} else {
			after = next
		}
		if ctx.Err() == nil {
			query, cancel := context.WithTimeout(ctx, 5*time.Second)
			nextReader, _, readerErr := d.db.RecoverTerminalReadersPage(query, readerAfter, 32, time.Now().UTC())
			cancel()
			// The internal rowid cursor advances even past an ineligible or
			// corrupt candidate; one bad record must not pin later pages.
			readerAfter = nextReader
			d.supervisorHeartbeat(-1)
			if readerErr != nil && !transientSupervisorError(readerErr) && ctx.Err() == nil {
				d.recordError()
			}
		}
		if ctx.Err() == nil && d.workboardRecovery != nil {
			query, cancel := context.WithTimeout(ctx, 5*time.Second)
			nextWorkboard, _, workboardErr := d.workboardRecovery.RecoverAttentionPage(query, workboardAfter)
			cancel()
			d.supervisorHeartbeat(-1)
			if workboardErr != nil && !transientSupervisorError(workboardErr) {
				if ctx.Err() == nil {
					d.recordError()
				}
			} else {
				workboardAfter = nextWorkboard
			}
		}
		if ctx.Err() == nil {
			query, cancel := context.WithTimeout(ctx, 5*time.Second)
			nextAttention, _, attentionErr := d.db.SweepLeaseAttentionPage(query, attentionAfter, time.Now().UTC(), 32)
			cancel()
			// Failed candidates remain visible but do not starve later leases.
			// The sweep preserves the input cursor on selection failure and
			// advances only past attempted candidates on partial failure.
			attentionAfter = nextAttention
			d.supervisorHeartbeat(-1)
			if attentionErr != nil && !transientSupervisorError(attentionErr) {
				if ctx.Err() == nil {
					d.recordError()
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// reconcileConfigurationPage never executes old-generation work. Safe
// pre-start rows are retired, while started rows may only use the existing
// history-derived terminal/interruption recovery paths under their own digest.
func (d *Dispatcher) reconcileConfigurationPage(ctx context.Context, currentDigest, after string) (string, error) {
	page, pageErr := d.db.ConfigurationMismatchCandidatesPage(ctx, currentDigest, after, 100, time.Now().UTC())
	permanentErr := pageErr
	for _, item := range page.Items {
		if item.State == "running" {
			recovered, recoverErr := d.db.RecoverTerminalSubmission(ctx, item.ID, item.ConfigDigest, time.Now().UTC())
			if recoverErr != nil {
				if permanentReconciliationError(recoverErr) {
					permanentErr = recoverErr
					continue
				}
				return after, recoverErr
			}
			if !recovered {
				commit, commitErr := d.recoverInterruptedModel(ctx, item, item.ConfigDigest)
				recovered, recoverErr = commit.Changed, commitErr
			}
			if recoverErr != nil {
				if permanentReconciliationError(recoverErr) {
					permanentErr = recoverErr
					continue
				}
				return after, recoverErr
			}
			if !recovered {
				commit, commitErr := d.recoverInterruptedDelegation(ctx, item, item.ConfigDigest)
				recovered, recoverErr = commit.Changed, commitErr
			}
			if recoverErr != nil {
				if permanentReconciliationError(recoverErr) {
					permanentErr = recoverErr
					continue
				}
				return after, recoverErr
			}
			if recovered {
				continue
			}
		}
		if _, retireErr := d.db.RetireConfigurationMismatch(ctx, item.ID, currentDigest, time.Now().UTC()); retireErr != nil {
			if errors.Is(retireErr, submissions.ErrInvalid) {
				permanentErr = retireErr
				continue
			}
			return after, retireErr
		}
	}
	return page.NextCursor, permanentErr
}

// One bounded page per tick avoids monopolizing the writer even when a large
// legacy running population is ineligible for recovery.
func (d *Dispatcher) recoverPage(ctx context.Context, configDigest, after string) (string, error) {
	page, err := d.db.ListSubmissions(ctx, submissions.ListOptions{State: "running", After: after, Limit: 100})
	if err != nil {
		return after, err
	}
	for _, item := range page.Items {
		if item.ConfigDigest != configDigest || !item.LeaseExpired {
			continue
		}
		var err error
		if len(item.TaskIDs) == 0 {
			_, err = d.db.RecoverUndispatched(ctx, item.ID, configDigest, time.Now().UTC())
		} else {
			var recovered bool
			recovered, err = d.db.RecoverTerminalSubmission(ctx, item.ID, configDigest, time.Now().UTC())
			if err == nil && !recovered {
				commit, commitErr := d.recoverInterruptedModel(ctx, item, configDigest)
				recovered, err = commit.Changed, commitErr
			}
			if err == nil && !recovered {
				_, commitErr := d.recoverInterruptedDelegation(ctx, item, configDigest)
				err = commitErr
			}
			if permanentReconciliationError(err) {
				d.recordError()
				err = nil
			}
		}
		if err != nil {
			return after, err
		}
	}
	if page.HasMore {
		return page.NextCursor, nil
	}
	return "", nil
}

func permanentReconciliationError(err error) bool {
	return errors.Is(err, submissions.ErrInvalid) || errors.Is(err, telemetry.ErrRecoveryRedaction) || errors.Is(err, errRecoverySecretResolution)
}
