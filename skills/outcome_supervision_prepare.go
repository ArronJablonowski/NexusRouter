package skills

import (
	"context"
	"errors"
	"math"
	"sort"
	"time"
)

// PrepareOutcomeSupervision reserves at most one exact active skill for a
// named supervisor. Existing pending work is returned unchanged and a future
// due time returns an empty check. The method never creates outcome rollback
// intents, selection checkpoints, or receipts.
func (s *FileStore) PrepareOutcomeSupervision(ctx context.Context, scope, name, policyDigest string, interval time.Duration, guard OutcomeSupervisionGuard) (OutcomeSupervisionState, OutcomeSupervisionCheck, error) {
	zero, empty := OutcomeSupervisionState{}, OutcomeSupervisionCheck{}
	if s == nil || ctx == nil || !s.permitted(Key{scope, name}) || guard == nil {
		return zero, empty, ErrInvalid
	}
	if s.readOnly || !s.automatic.Load() || !s.outcomeRollback.Load() {
		return zero, empty, ErrDisabled
	}
	now := time.Now().UTC()
	state := OutcomeSupervisionState{Version: 1, Scope: scope, Name: name, PolicyDigest: policyDigest, Interval: interval, NextDue: now}
	if state.Validate() != nil {
		return zero, empty, ErrInvalid
	}
	var check OutcomeSupervisionCheck
	err := s.with(ctx, func(c *catalog) error {
		if !s.automatic.Load() || !s.outcomeRollback.Load() {
			return ErrDisabled
		}
		index := (Key{scope, name}).index()
		old, exists := c.OutcomeSupervisors[index]
		if exists {
			if old.PolicyDigest != policyDigest || old.Interval != interval {
				return ErrConflict
			}
			state = old
			if old.PendingCheckID != "" {
				check = c.OutcomeSupervisorChecks[old.PendingCheckID]
				if err := runOutcomeSupervisionGuard(ctx, guard, state, check); err != nil {
					return err
				}
				return errCatalogUnchanged
			}
			if time.Now().Before(state.NextDue) {
				if err := runOutcomeSupervisionGuard(ctx, guard, state, check); err != nil {
					return err
				}
				return errCatalogUnchanged
			}
		} else if len(c.OutcomeSupervisors) >= 1000 {
			return ErrInvalid
		}
		if state.Revision >= math.MaxInt64-1 {
			return ErrInvalid
		}
		state.Revision++
		var candidate OutcomeRollbackCandidate
		for {
			next := ""
			for _, entry := range c.Skills {
				if entry.Key.Scope == scope && entry.Active != "" && entry.Key.Name > state.After &&
					(next == "" || entry.Key.Name < next) {
					next = entry.Key.Name
				}
			}
			if next == "" {
				break
			}
			entry := c.Skills[(Key{scope, next}).index()]
			expected, err := stateForEntry(entry)
			if err != nil {
				return err
			}
			candidate, err = outcomeRollbackCandidateForEntry(entry, expected)
			if err == nil {
				if outcomeSupervisionRevisionOwned(c, expected) {
					state.After = next
					candidate = OutcomeRollbackCandidate{}
					continue
				}
				break
			}
			if !errors.Is(err, ErrConflict) && !errors.Is(err, ErrValidation) {
				return err
			}
			// An active skill without an eligible predecessor is an ordinary scan
			// result. Persist its lexical progress in this same transaction.
			state.After = next
		}
		if candidate.Version == 0 {
			state.After = ""
			state.NextDue = time.Now().UTC().Add(interval)
		} else {
			if len(c.OutcomeSupervisorChecks) >= 1000 {
				ids := make([]string, 0, len(c.OutcomeSupervisorChecks))
				for id, prior := range c.OutcomeSupervisorChecks {
					if prior.Status != "pending" {
						ids = append(ids, id)
					}
				}
				sort.Slice(ids, func(i, j int) bool {
					a, b := c.OutcomeSupervisorChecks[ids[i]], c.OutcomeSupervisorChecks[ids[j]]
					if a.FinishedAt.Equal(b.FinishedAt) {
						return ids[i] < ids[j]
					}
					return a.FinishedAt.Before(b.FinishedAt)
				})
				if len(ids) == 0 {
					return ErrInvalid
				}
				delete(c.OutcomeSupervisorChecks, ids[0])
			}
			id := ""
			for range 8 {
				candidate := randomID()
				if _, used := c.OutcomeSupervisorChecks[candidate]; !used {
					id = candidate
					break
				}
			}
			if id == "" {
				return ErrConflict
			}
			check = OutcomeSupervisionCheck{Version: 1, Scope: scope, SupervisorName: name, PolicyDigest: policyDigest,
				CheckID: id, Sequence: state.Revision, Candidate: candidate, PreviousCursor: state.After,
				PreparedAt: time.Now().UTC(), Status: "pending"}
			state.PendingCheckID = id
		}
		if state.Validate() != nil || check.CheckID != "" && check.Validate() != nil {
			return ErrInvalid
		}
		if err := runOutcomeSupervisionGuard(ctx, guard, state, check); err != nil {
			return err
		}
		if c.OutcomeSupervisors == nil {
			c.OutcomeSupervisors = map[string]OutcomeSupervisionState{}
		}
		if check.CheckID != "" {
			if c.OutcomeSupervisorChecks == nil {
				c.OutcomeSupervisorChecks = map[string]OutcomeSupervisionCheck{}
			}
			c.OutcomeSupervisorChecks[check.CheckID] = check
		}
		c.OutcomeSupervisors[index] = state
		if c.Schema < 9 {
			c.Schema = 9
		}
		return nil
	}, true)
	if err != nil {
		return zero, empty, err
	}
	return state, check, nil
}

func outcomeSupervisionRevisionOwned(c *catalog, expected ActivationState) bool {
	for _, receipt := range c.OutcomeOperations {
		if receipt.Expected.Key == expected.Key && receipt.Expected.Revision == expected.Revision {
			return true
		}
	}
	for _, intent := range c.OutcomeIntents {
		if intent.Expected.Key == expected.Key && intent.Expected.Revision == expected.Revision {
			return true
		}
	}
	for _, settlement := range c.OutcomeSettlements {
		if settlement.Expected.Key == expected.Key && settlement.Expected.Revision == expected.Revision {
			return true
		}
	}
	return false
}
