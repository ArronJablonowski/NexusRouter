package skills

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// SetOutcomeRollback controls an independent default-off permission. Both this
// flag and SetAutomatic must be enabled for outcome-policy operations/retries.
func (s *FileStore) SetOutcomeRollback(enabled bool) {
	if s != nil {
		s.outcomeRollback.Store(enabled)
	}
}

func (s *FileStore) OutcomeRollbackOnce(ctx context.Context, id string, expected ActivationState, policy ComparisonSelectionPolicy, selector OutcomeSelector, guard OutcomeRollbackGuard) (out OutcomeRollbackReceipt, err error) {
	defer func() {
		if recover() != nil {
			out, err = OutcomeRollbackReceipt{}, ErrInvalid
		}
	}()
	if s == nil || ctx == nil || !s.permitted(expected.Key) || !identifier.MatchString(id) || expected.Validate() != nil || policy.Validate() != nil || policy.Comparison.Key != expected.Key || policy.Comparison.CandidateVersion != expected.Active || selector == nil || guard == nil {
		return out, ErrInvalid
	}
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	if s.readOnly || !s.automatic.Load() || !s.outcomeRollback.Load() {
		return out, ErrDisabled
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	found := false
	err = s.with(ctx, func(c *catalog) error {
		r, e := matchingOutcomeOperation(c, id, expected, policy)
		if e == nil {
			if e = s.outcomeGuard(ctx, guard, r); e != nil {
				return e
			}
			out = r
			found = true
			return nil
		}
		if !errors.Is(e, ErrNotFound) {
			return e
		}
		if len(c.OutcomeOperations) >= 1000 {
			return ErrInvalid
		}
		entry, ok := c.Skills[expected.Key.index()]
		if !ok {
			return ErrNotFound
		}
		return outcomeCandidate(entry, expected, policy)
	}, false)
	if err != nil {
		return OutcomeRollbackReceipt{}, err
	}
	if found {
		return out, nil
	}
	// Verify both immutable version files before selecting any evidence. Catalog
	// metadata/proofs and the exact activation fence are checked again at commit.
	for _, version := range []string{policy.Comparison.BaselineVersion, expected.Active} {
		if _, err = s.Load(ctx, expected.Key, version); err != nil {
			return OutcomeRollbackReceipt{}, err
		}
	}
	selection, err := selector(ctx)
	if err != nil {
		return OutcomeRollbackReceipt{}, err
	}
	if ctx.Err() != nil {
		return OutcomeRollbackReceipt{}, ctx.Err()
	}
	if selection.Validate() != nil || selection.Policy != policy {
		return OutcomeRollbackReceipt{}, ErrInvalid
	}
	// Own all callback-returned maps/pointers before validation and persistence.
	body, err := json.Marshal(selection)
	if err != nil || len(body) > 64<<10 {
		return OutcomeRollbackReceipt{}, ErrInvalid
	}
	var frozen ComparisonSelectionReport
	if json.Unmarshal(body, &frozen) != nil {
		return OutcomeRollbackReceipt{}, ErrInvalid
	}
	selection = frozen
	err = s.with(ctx, func(c *catalog) error {
		if !s.automatic.Load() || !s.outcomeRollback.Load() {
			return ErrDisabled
		}
		r, e := matchingOutcomeOperation(c, id, expected, policy)
		if e == nil {
			if e = s.outcomeGuard(ctx, guard, r); e != nil {
				return e
			}
			out = r
			return errCatalogUnchanged
		}
		if !errors.Is(e, ErrNotFound) {
			return e
		}
		if len(c.OutcomeOperations) >= 1000 {
			return ErrInvalid
		}
		entry, ok := c.Skills[expected.Key.index()]
		if !ok {
			return ErrNotFound
		}
		if e = outcomeCandidate(entry, expected, policy); e != nil {
			return e
		}
		if e = outcomeDigests(entry, selection); e != nil {
			return e
		}
		out = OutcomeRollbackReceipt{Version: 1, OperationID: id, Expected: expected, Policy: policy, Selection: selection, After: expected, ActivationCount: len(entry.Activations), CheckedAt: time.Now().UTC(), Decision: "no_action"}
		if selection.Comparison != nil && selection.Comparison.Status == "regression_signal" {
			if len(entry.Activations) >= 10000 {
				return ErrInvalid
			}
			out.Decision = "rolled_back"
			entry.Activations = append(entry.Activations, activation{From: expected.Active, To: policy.Comparison.BaselineVersion, At: out.CheckedAt, Rollback: true, OutcomeOperationID: id})
			entry.Active = policy.Comparison.BaselineVersion
			out.After, e = stateForEntry(entry)
			if e != nil {
				return e
			}
		}
		if out.Validate() != nil {
			return ErrInvalid
		}
		if e = s.outcomeGuard(ctx, guard, out); e != nil {
			return e
		}
		if c.OutcomeOperations == nil {
			c.OutcomeOperations = map[string]OutcomeRollbackReceipt{}
		}
		c.OutcomeOperations[id] = out
		c.Skills[expected.Key.index()] = entry
		if c.Schema < 6 {
			c.Schema = 6
		}
		if e = validateOutcomeOperations(c); e != nil {
			return e
		}
		_, e = lookupOutcomeOperation(c, expected.Key, id)
		return e
	}, true)
	if err != nil {
		return OutcomeRollbackReceipt{}, err
	}
	return out, nil
}

func matchingOutcomeOperation(c *catalog, id string, expected ActivationState, policy ComparisonSelectionPolicy) (OutcomeRollbackReceipt, error) {
	r, err := lookupOutcomeOperation(c, expected.Key, id)
	if err == nil {
		if r.Expected != expected || r.Policy != policy {
			return OutcomeRollbackReceipt{}, ErrConflict
		}
		return r, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return OutcomeRollbackReceipt{}, err
	}
	for _, prior := range c.OutcomeOperations {
		if prior.Expected.Key == expected.Key && prior.Expected.Revision == expected.Revision {
			return OutcomeRollbackReceipt{}, ErrConflict
		}
	}
	return OutcomeRollbackReceipt{}, ErrNotFound
}

func outcomeCandidate(e entry, expected ActivationState, policy ComparisonSelectionPolicy) error {
	state, err := stateForEntry(e)
	if err != nil {
		return err
	}
	if state != expected {
		return ErrConflict
	}
	stack, err := activationStack(e)
	if err != nil {
		return err
	}
	if len(stack) == 0 || stack[len(stack)-1].From != policy.Comparison.BaselineVersion || stack[len(stack)-1].To != expected.Active {
		return ErrConflict
	}
	count := 0
	for _, a := range e.Activations {
		if a.To == expected.Active {
			count++
		}
	}
	if count != 1 || len(e.Activations) == 0 || e.Activations[len(e.Activations)-1].Rollback || e.Activations[len(e.Activations)-1].To != expected.Active {
		return ErrConflict
	}
	proof := e.Validated[policy.Comparison.BaselineVersion]
	if !proof.Passed || !proof.Deterministic || !identifier.MatchString(proof.ID) {
		return ErrValidation
	}
	return nil
}

func outcomeDigests(e entry, selection ComparisonSelectionReport) error {
	if selection.Comparison == nil {
		return nil
	}
	for _, cohort := range []ComparisonCohort{selection.Comparison.Baseline, selection.Comparison.Candidate} {
		if cohort.Digest == "" {
			continue
		}
		found := false
		for _, metadata := range e.Versions {
			if metadata.Version == cohort.Version && metadata.Digest == cohort.Digest {
				found = true
			}
		}
		if !found {
			return ErrInvalid
		}
	}
	return nil
}

func (s *FileStore) outcomeGuard(ctx context.Context, guard OutcomeRollbackGuard, r OutcomeRollbackReceipt) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrInvalid
		}
	}()
	if !s.automatic.Load() || !s.outcomeRollback.Load() {
		return ErrDisabled
	}
	body, err := json.Marshal(r)
	if err != nil {
		return ErrInvalid
	}
	var owned OutcomeRollbackReceipt
	if json.Unmarshal(body, &owned) != nil {
		return ErrInvalid
	}
	if err = guard(ctx, owned); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if !s.automatic.Load() || !s.outcomeRollback.Load() {
		return ErrDisabled
	}
	return nil
}
