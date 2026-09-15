package skills

import "context"

// BindOutcomeSupervisionOperation durably attaches the deterministic outcome
// operation identifier before any selector or rollback action is dispatched.
// A restart can therefore inspect either the exact receipt or the still-pending
// action without reconstructing the activation candidate from mutable state.
func (s *FileStore) BindOutcomeSupervisionOperation(ctx context.Context, check OutcomeSupervisionCheck, operationID string, guard OutcomeSupervisionGuard) (OutcomeSupervisionCheck, error) {
	if s == nil || ctx == nil || check.Validate() != nil || check.Status != "pending" ||
		check.OutcomeOperationID != "" || !identifier.MatchString(operationID) ||
		!s.permitted(Key{check.Scope, check.SupervisorName}) || guard == nil {
		return OutcomeSupervisionCheck{}, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return OutcomeSupervisionCheck{}, err
	}
	if s.readOnly || !s.automatic.Load() || !s.outcomeRollback.Load() {
		return OutcomeSupervisionCheck{}, ErrDisabled
	}
	var out OutcomeSupervisionCheck
	err := s.with(ctx, func(c *catalog) error {
		if !s.automatic.Load() || !s.outcomeRollback.Load() {
			return ErrDisabled
		}
		stored, ok := c.OutcomeSupervisorChecks[check.CheckID]
		if !ok {
			return ErrConflict
		}
		state, ok := c.OutcomeSupervisors[(Key{stored.Scope, stored.SupervisorName}).index()]
		if !ok || state.PendingCheckID != stored.CheckID || stored.Status != "pending" ||
			!sameOutcomeSupervisionBinding(stored, check) {
			return ErrConflict
		}
		if stored.OutcomeOperationID != "" {
			if stored.OutcomeOperationID != operationID {
				return ErrConflict
			}
			out = stored
			if err := runOutcomeSupervisionGuard(ctx, guard, state, out); err != nil {
				return err
			}
			return errCatalogUnchanged
		}
		current, err := stateForEntry(c.Skills[stored.Candidate.Current.Key.index()])
		if err != nil || current != stored.Candidate.Current {
			return ErrConflict
		}
		for id, other := range c.OutcomeSupervisorChecks {
			if id != stored.CheckID && other.OutcomeOperationID == operationID {
				return ErrConflict
			}
		}
		stored.OutcomeOperationID = operationID
		if stored.Validate() != nil {
			return ErrInvalid
		}
		if err := runOutcomeSupervisionGuard(ctx, guard, state, stored); err != nil {
			return err
		}
		c.OutcomeSupervisorChecks[stored.CheckID] = stored
		out = stored
		return validateOutcomeSupervision(c)
	}, true)
	if err != nil {
		return OutcomeSupervisionCheck{}, err
	}
	return out, nil
}

func sameOutcomeSupervisionBinding(a, b OutcomeSupervisionCheck) bool {
	a.OutcomeOperationID, b.OutcomeOperationID = "", ""
	return sameOutcomeSupervisionIntent(a, b)
}
