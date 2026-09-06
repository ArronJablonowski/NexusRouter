package skills

import (
	"context"
	"errors"
	"math"
	"time"
)

type regressionMonitorExecution struct {
	check       RegressionMonitorCheck
	guard       RegressionMonitorGuard
	failureCode string
}

func sameRegressionMonitorIntent(a, b RegressionMonitorCheck) bool {
	a.Status, b.Status = "", ""
	a.Code, b.Code = "", ""
	a.FinishedAt, b.FinishedAt = time.Time{}, time.Time{}
	return a == b
}

// Every registered operation is reserved, including terminal failures. Neither
// public operation retries nor a late monitor validator can bypass this fence.
func regressionMonitorFence(ctx context.Context, c *catalog, id string, run *regressionMonitorExecution) error {
	p, owned := c.RegressionMonitorChecks[id]
	if !owned {
		if run != nil {
			return ErrConflict
		}
		return nil
	}
	if run == nil || !sameRegressionMonitorIntent(p, run.check) {
		return ErrConflict
	}
	s := c.RegressionMonitors[(Key{p.Scope, p.MonitorName}).index()]
	if p.Status == "failed" {
		return ErrConflict
	}
	if p.Status == "pending" && (s.PendingOperationID != id || s.Revision != p.Sequence) {
		return ErrConflict
	}
	return runRegressionMonitorGuard(ctx, run.guard, s, p)
}

// ExecuteRegressionMonitorCheck resumes the exact durable intent. A stored
// receipt wins over any concurrent callback failure. Audited failures advance
// fairly while retaining tombstones which fence late callbacks from committing.
// Unknown storage errors and cancellation preserve pending work for inspection.
func (s *FileStore) ExecuteRegressionMonitorCheck(ctx context.Context, check RegressionMonitorCheck, validator Validator, guard RegressionMonitorGuard) (RegressionMonitorState, error) {
	zero := RegressionMonitorState{}
	if s == nil || ctx == nil || check.Validate() != nil || !s.permitted(Key{check.Scope, check.MonitorName}) || guard == nil {
		return zero, ErrInvalid
	}
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	if s.readOnly || !s.automatic.Load() {
		return zero, ErrDisabled
	}
	if validator == nil || nilRegressionValidator(validator) {
		return zero, ErrValidation
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var state RegressionMonitorState
	terminal := false
	err := s.with(ctx, func(c *catalog) error {
		if !s.automatic.Load() {
			return ErrDisabled
		}
		p, ok := c.RegressionMonitorChecks[check.OperationID]
		if !ok || !sameRegressionMonitorIntent(p, check) {
			return ErrConflict
		}
		state = c.RegressionMonitors[(Key{p.Scope, p.MonitorName}).index()]
		terminal = p.Status != "pending"
		if p.Status == "completed" {
			if _, err := matchingRegressionOperation(c, p.OperationID, p.ValidatorID, p.Expected); err != nil {
				return err
			}
		}
		return runRegressionMonitorGuard(ctx, guard, state, p)
	}, false)
	if err != nil {
		return zero, err
	}
	if terminal {
		return state, nil
	}
	run := &regressionMonitorExecution{check: check, guard: guard}
	_, runErr := s.revalidateAndRollbackOnce(ctx, check.OperationID, check.ValidatorID, check.Expected, validator, run)
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	code := ""
	if run.failureCode == "stale_activation" && errors.Is(runErr, ErrConflict) {
		code = run.failureCode
	}
	if run.failureCode == "check_failed" && (errors.Is(runErr, ErrValidation) || errors.Is(runErr, ErrNotFound)) {
		code = run.failureCode
	}
	resolved := false
	err = s.with(ctx, func(c *catalog) error {
		if !s.automatic.Load() {
			return ErrDisabled
		}
		p, ok := c.RegressionMonitorChecks[check.OperationID]
		if !ok || !sameRegressionMonitorIntent(p, check) {
			return ErrConflict
		}
		index := (Key{p.Scope, p.MonitorName}).index()
		state = c.RegressionMonitors[index]
		o, receiptErr := matchingRegressionOperation(c, p.OperationID, p.ValidatorID, p.Expected)
		if receiptErr != nil && !errors.Is(receiptErr, ErrNotFound) {
			return receiptErr
		}
		if p.Status != "pending" {
			if p.Status == "completed" && receiptErr != nil || p.Status == "failed" && receiptErr == nil {
				return ErrInvalid
			}
			if err := runRegressionMonitorGuard(ctx, guard, state, p); err != nil {
				return err
			}
			resolved = true
			return errCatalogUnchanged
		}
		if state.PendingOperationID != p.OperationID || state.Revision != p.Sequence {
			return ErrConflict
		}
		if receiptErr != nil && code == "" {
			if err := runRegressionMonitorGuard(ctx, guard, state, p); err != nil {
				return err
			}
			return errCatalogUnchanged
		}
		if state.Revision == math.MaxInt64 {
			return ErrInvalid
		}
		p.Status = "completed"
		if receiptErr != nil {
			p.Status = "failed"
			p.Code = code
		} else if o.Expected != p.Expected {
			return ErrInvalid
		}
		p.FinishedAt = time.Now().UTC()
		state.Revision++
		state.After = p.Expected.Key.Name
		state.PendingOperationID = ""
		state.NextDue = p.FinishedAt.Add(state.Interval)
		if state.Validate() != nil || p.Validate() != nil {
			return ErrInvalid
		}
		if err := runRegressionMonitorGuard(ctx, guard, state, p); err != nil {
			return err
		}
		c.RegressionMonitorChecks[p.OperationID] = p
		c.RegressionMonitors[index] = state
		resolved = true
		return nil
	}, true)
	if err != nil {
		return zero, err
	}
	if !resolved {
		if runErr != nil {
			return zero, runErr
		}
		return zero, ErrInvalid
	}
	return state, nil
}
