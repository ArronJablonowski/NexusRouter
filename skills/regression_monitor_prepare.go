package skills

import (
	"context"
	"math"
	"time"
)

// PrepareRegressionMonitor durably reserves one exact activation before any
// validator runs. A named monitor's policy, validator and interval are immutable.
// Existing pending work is returned unchanged; a future due time returns no work.
func (s *FileStore) PrepareRegressionMonitor(ctx context.Context, scope, name, validatorID, policyDigest string, interval time.Duration, guard RegressionMonitorGuard) (RegressionMonitorState, RegressionMonitorCheck, error) {
	zero, empty := RegressionMonitorState{}, RegressionMonitorCheck{}
	if s == nil || ctx == nil || !s.permitted(Key{scope, name}) || guard == nil {
		return zero, empty, ErrInvalid
	}
	if s.readOnly || !s.automatic.Load() {
		return zero, empty, ErrDisabled
	}
	state := RegressionMonitorState{Version: 1, Scope: scope, Name: name, ValidatorID: validatorID, PolicyDigest: policyDigest, Interval: interval, NextDue: time.Now().UTC()}
	if state.Validate() != nil {
		return zero, empty, ErrInvalid
	}
	var check RegressionMonitorCheck
	err := s.with(ctx, func(c *catalog) error {
		if !s.automatic.Load() {
			return ErrDisabled
		}
		index := (Key{scope, name}).index()
		old, exists := c.RegressionMonitors[index]
		if exists {
			if old.ValidatorID != validatorID || old.PolicyDigest != policyDigest || old.Interval != interval {
				return ErrConflict
			}
			state = old
			if old.PendingOperationID != "" {
				check = c.RegressionMonitorChecks[old.PendingOperationID]
				if err := runRegressionMonitorGuard(ctx, guard, state, check); err != nil {
					return err
				}
				return errCatalogUnchanged
			}
			if time.Now().Before(state.NextDue) {
				if err := runRegressionMonitorGuard(ctx, guard, state, check); err != nil {
					return err
				}
				return errCatalogUnchanged
			}
		} else if len(c.RegressionMonitors) >= 1000 {
			return ErrInvalid
		}
		// Reserve a revision for terminal completion as well as preparation.
		if state.Revision >= math.MaxInt64-1 {
			return ErrInvalid
		}
		next := ""
		for _, e := range c.Skills {
			if e.Key.Scope == scope && e.Active != "" && e.Key.Name > state.After && (next == "" || e.Key.Name < next) {
				next = e.Key.Name
			}
		}
		state.Revision++
		if next == "" {
			state.After = ""
			state.NextDue = time.Now().UTC().Add(interval)
		} else {
			if len(c.RegressionMonitorChecks) >= 1000 || len(c.RegressionOperations) >= 1000 {
				return ErrInvalid
			}
			expected, err := stateForEntry(c.Skills[(Key{scope, next}).index()])
			if err != nil {
				return err
			}
			id := ""
			for i := 0; i < 8; i++ {
				candidate := randomID()
				_, receipt := c.RegressionOperations[candidate]
				_, used := c.RegressionMonitorChecks[candidate]
				if !receipt && !used {
					id = candidate
					break
				}
			}
			if id == "" {
				return ErrConflict
			}
			check = RegressionMonitorCheck{Version: 1, Scope: scope, MonitorName: name, ValidatorID: validatorID, PolicyDigest: policyDigest, OperationID: id, Sequence: state.Revision, Expected: expected, PreviousCursor: state.After, PreparedAt: time.Now().UTC(), Status: "pending"}
			state.PendingOperationID = id
		}
		if state.Validate() != nil || check.OperationID != "" && check.Validate() != nil {
			return ErrInvalid
		}
		if err := runRegressionMonitorGuard(ctx, guard, state, check); err != nil {
			return err
		}
		if c.RegressionMonitors == nil {
			c.RegressionMonitors = map[string]RegressionMonitorState{}
		}
		if check.OperationID != "" {
			if c.RegressionMonitorChecks == nil {
				c.RegressionMonitorChecks = map[string]RegressionMonitorCheck{}
			}
			c.RegressionMonitorChecks[check.OperationID] = check
		}
		c.RegressionMonitors[index] = state
		if c.Schema < 5 {
			c.Schema = 5
		}
		return nil
	}, true)
	if err != nil {
		return zero, empty, err
	}
	return state, check, nil
}
