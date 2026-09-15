package skills

import (
	"context"
	"errors"
	"math"
	"time"
)

func sameOutcomeSupervisionIntent(a, b OutcomeSupervisionCheck) bool {
	a.Status, b.Status = "", ""
	a.Code, b.Code = "", ""
	a.FinishedAt, b.FinishedAt = time.Time{}, time.Time{}
	return a == b
}

func completionMatchesOutcomeSupervisionCheck(c OutcomeSupervisionCompletion, check OutcomeSupervisionCheck) bool {
	return c.Code == check.Code && c.OutcomeOperationID == check.OutcomeOperationID
}

// CompleteOutcomeSupervisionCheck commits one scheduling outcome. Waiting and
// ineligible completions advance the lexical cursor without touching outcome
// rollback records. Evaluated completion is accepted only when its immutable
// outcome receipt already exists and binds the reserved activation.
func (s *FileStore) CompleteOutcomeSupervisionCheck(ctx context.Context, check OutcomeSupervisionCheck, completion OutcomeSupervisionCompletion, guard OutcomeSupervisionGuard) (OutcomeSupervisionState, error) {
	zero := OutcomeSupervisionState{}
	if s == nil || ctx == nil || check.Validate() != nil || check.Status != "pending" ||
		completion.Validate() != nil || !s.permitted(Key{check.Scope, check.SupervisorName}) || guard == nil {
		return zero, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if s.readOnly || !s.automatic.Load() || !s.outcomeRollback.Load() {
		return zero, ErrDisabled
	}
	var state OutcomeSupervisionState
	err := s.with(ctx, func(c *catalog) error {
		if !s.automatic.Load() || !s.outcomeRollback.Load() {
			return ErrDisabled
		}
		stored, ok := c.OutcomeSupervisorChecks[check.CheckID]
		if !ok || !sameOutcomeSupervisionIntent(stored, check) {
			return ErrConflict
		}
		index := (Key{stored.Scope, stored.SupervisorName}).index()
		state, ok = c.OutcomeSupervisors[index]
		if !ok {
			return ErrInvalid
		}
		if stored.Status != "pending" {
			if !completionMatchesOutcomeSupervisionCheck(completion, stored) {
				return ErrConflict
			}
			if err := runOutcomeSupervisionGuard(ctx, guard, state, stored); err != nil {
				return err
			}
			return errCatalogUnchanged
		}
		if state.PendingCheckID != stored.CheckID || state.Revision != stored.Sequence || state.PolicyDigest != stored.PolicyDigest {
			return ErrConflict
		}
		entry, exists := c.Skills[stored.Candidate.Current.Key.index()]
		if !exists {
			return ErrInvalid
		}
		current, currentErr := stateForEntry(entry)
		if currentErr != nil {
			return currentErr
		}
		switch completion.Code {
		case "waiting", "ineligible":
			if stored.OutcomeOperationID != "" || current != stored.Candidate.Current {
				return ErrConflict
			}
		case "evaluated":
			if stored.OutcomeOperationID == "" || completion.OutcomeOperationID != stored.OutcomeOperationID {
				return ErrConflict
			}
			receipt, receiptErr := lookupOutcomeOperation(c, stored.Candidate.Current.Key, completion.OutcomeOperationID)
			if receiptErr != nil || receipt.Expected != stored.Candidate.Current {
				if receiptErr != nil && !errors.Is(receiptErr, ErrNotFound) {
					return receiptErr
				}
				return ErrConflict
			}
		case "stale_activation":
			if completion.OutcomeOperationID != stored.OutcomeOperationID || current == stored.Candidate.Current {
				return ErrConflict
			}
		case "check_failed":
			if completion.OutcomeOperationID != stored.OutcomeOperationID {
				return ErrConflict
			}
		}
		if state.Revision == math.MaxInt64 {
			return ErrInvalid
		}
		stored.Status = "completed"
		if completion.Code == "check_failed" {
			stored.Status = "failed"
		}
		stored.Code = completion.Code
		stored.OutcomeOperationID = completion.OutcomeOperationID
		stored.FinishedAt = time.Now().UTC()
		state.Revision++
		state.After = stored.Candidate.Current.Key.Name
		state.PendingCheckID = ""
		state.NextDue = stored.FinishedAt.Add(state.Interval)
		if stored.Validate() != nil || state.Validate() != nil {
			return ErrInvalid
		}
		if err := runOutcomeSupervisionGuard(ctx, guard, state, stored); err != nil {
			return err
		}
		c.OutcomeSupervisorChecks[stored.CheckID] = stored
		c.OutcomeSupervisors[index] = state
		return validateOutcomeSupervision(c)
	}, true)
	if err != nil {
		return zero, err
	}
	return state, nil
}
