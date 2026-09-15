package skills

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
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
	if s == nil || ctx == nil || !s.permitted(expected.Key) || !identifier.MatchString(id) || expected.Validate() != nil || policy.Validate() != nil || policy.Comparison.Key != expected.Key || policy.Comparison.CandidateVersion != expected.Active || selector == nil || guard == nil {
		return out, ErrInvalid
	}
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	if s.readOnly || !s.automatic.Load() || !s.outcomeRollback.Load() {
		return out, ErrDisabled
	}
	modelID := ""
	prior, e := s.OutcomeRollbackOperation(ctx, expected.Key, id)
	if e == nil {
		if prior.Expected != expected || prior.Policy != policy {
			return out, ErrConflict
		}
		modelID = prior.Selection.ConfiguredModelID
	} else if !errors.Is(e, ErrNotFound) {
		return out, e
	}
	return s.OutcomeRollbackOnceGuarded(ctx, id, modelID, expected, policy, selector, guard, func(context.Context, OutcomeRollbackIntent) error { return nil })
}

func (s *FileStore) OutcomeRollbackOnceGuarded(ctx context.Context, id, configuredModelID string, expected ActivationState, policy ComparisonSelectionPolicy, selector OutcomeSelector, guard OutcomeRollbackGuard, intentGuard OutcomeIntentGuard) (out OutcomeRollbackReceipt, err error) {
	return s.outcomeRollbackOnce(ctx, id, configuredModelID, expected, policy, selector, guard, intentGuard, nil)
}

func (s *FileStore) OutcomeRollbackOnceCheckpointed(ctx context.Context, id, configuredModelID string, expected ActivationState, policy ComparisonSelectionPolicy, selector OutcomeSelector, guard OutcomeRollbackGuard, intentGuard OutcomeIntentGuard, selectionGuard OutcomeSelectionGuard) (OutcomeRollbackReceipt, error) {
	if selectionGuard == nil {
		return OutcomeRollbackReceipt{}, ErrInvalid
	}
	return s.outcomeRollbackOnce(ctx, id, configuredModelID, expected, policy, selector, guard, intentGuard, selectionGuard)
}

func (s *FileStore) outcomeRollbackOnce(ctx context.Context, id, configuredModelID string, expected ActivationState, policy ComparisonSelectionPolicy, selector OutcomeSelector, guard OutcomeRollbackGuard, intentGuard OutcomeIntentGuard, selectionGuard OutcomeSelectionGuard) (out OutcomeRollbackReceipt, err error) {
	defer func() {
		if recover() != nil {
			out, err = OutcomeRollbackReceipt{}, ErrInvalid
		}
	}()
	if s == nil || ctx == nil || !s.permitted(expected.Key) || !identifier.MatchString(id) || configuredModelID != "" && !identifier.MatchString(configuredModelID) || expected.Validate() != nil || policy.Validate() != nil || policy.Comparison.Key != expected.Key || policy.Comparison.CandidateVersion != expected.Active || selector == nil || guard == nil || intentGuard == nil {
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
	var checkpoint OutcomeSelectionCheckpoint
	var claim OutcomeRollbackIntent
	var selection ComparisonSelectionReport
	err = s.with(ctx, func(c *catalog) error {
		r, e := matchingOutcomeOperation(c, id, expected, policy)
		if e == nil {
			if r.Selection.ConfiguredModelID != configuredModelID {
				return ErrConflict
			}
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
		if _, e = matchingOutcomeIntent(c, id, configuredModelID, expected, policy); e == nil {
			checkpoint, e = lookupOutcomeSelection(c, expected.Key, id)
			if errors.Is(e, ErrNotFound) {
				return ErrOutcomePending
			}
			return e
		} else if !errors.Is(e, ErrNotFound) {
			return e
		}
		if len(c.OutcomeOperations) >= 1000 {
			return ErrInvalid
		}
		if e = validateOutcomeAdmission(ctx, c); e != nil {
			return e
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
	if checkpoint.Version != 0 && selectionGuard == nil {
		return OutcomeRollbackReceipt{}, ErrOutcomePending
	}
	// Pending actions, including checkpoint resumes, require readable immutable
	// bodies. Completed historical receipts above remain file-independent.
	for _, version := range []string{policy.Comparison.BaselineVersion, expected.Active} {
		if _, err = s.Load(ctx, expected.Key, version); err != nil {
			return OutcomeRollbackReceipt{}, err
		}
	}
	if checkpoint.Version == 0 {
		// Reserve this revision durably before the selector can observe outcomes.
		// A write error or cancellation suppresses dispatch even if replacement may
		// have happened; later calls inspect the durable claim and never reselect.
		err = s.with(ctx, func(c *catalog) error {
			if !s.automatic.Load() || !s.outcomeRollback.Load() {
				return ErrDisabled
			}
			r, e := matchingOutcomeOperation(c, id, expected, policy)
			if e == nil {
				if r.Selection.ConfiguredModelID != configuredModelID {
					return ErrConflict
				}
				if e = s.outcomeGuard(ctx, guard, r); e != nil {
					return e
				}
				out = r
				found = true
				return errCatalogUnchanged
			}
			if !errors.Is(e, ErrNotFound) {
				return e
			}
			if _, e = matchingOutcomeIntent(c, id, configuredModelID, expected, policy); e == nil {
				return ErrOutcomePending
			} else if !errors.Is(e, ErrNotFound) {
				return e
			}
			if len(c.OutcomeOperations) >= 1000 || len(c.OutcomeIntents) >= 1000 {
				return ErrInvalid
			}
			if e = validateOutcomeAdmission(ctx, c); e != nil {
				return e
			}
			entry, ok := c.Skills[expected.Key.index()]
			if !ok {
				return ErrNotFound
			}
			if e = outcomeCandidate(entry, expected, policy); e != nil {
				return e
			}
			claim = OutcomeRollbackIntent{Version: 1, OperationID: id, ConfiguredModelID: configuredModelID, Expected: expected, Policy: policy, ActivationCount: len(entry.Activations), PreparedAt: time.Now().UTC()}
			if claim.Validate() != nil {
				return ErrInvalid
			}
			if e = intentGuard(ctx, claim); e != nil {
				return e
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if !s.automatic.Load() || !s.outcomeRollback.Load() {
				return ErrDisabled
			}
			if c.OutcomeIntents == nil {
				c.OutcomeIntents = map[string]OutcomeRollbackIntent{}
			}
			c.OutcomeIntents[id] = claim
			if c.Schema < 7 {
				c.Schema = 7
			}
			return validateOutcomeIntents(c)
		}, true)
		if err != nil {
			return OutcomeRollbackReceipt{}, err
		}
		if found {
			return out, nil
		}
		if ctx.Err() != nil {
			return OutcomeRollbackReceipt{}, ctx.Err()
		}
		if !s.automatic.Load() || !s.outcomeRollback.Load() {
			return OutcomeRollbackReceipt{}, ErrDisabled
		}
		selection, err = selector(ctx)
		if err != nil {
			return OutcomeRollbackReceipt{}, err
		}
		if ctx.Err() != nil {
			return OutcomeRollbackReceipt{}, ctx.Err()
		}
		if selection.Validate() != nil || selection.Policy != policy || selection.ConfiguredModelID != configuredModelID {
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
		if selectionGuard != nil {
			checkpoint, err = s.saveOutcomeSelection(ctx, claim, selection, selectionGuard)
			if err != nil {
				return OutcomeRollbackReceipt{}, err
			}
		}
	} else {
		claim = checkpoint.Intent
		selection = checkpoint.Report
	}
	err = s.with(ctx, func(c *catalog) error {
		if !s.automatic.Load() || !s.outcomeRollback.Load() {
			return ErrDisabled
		}
		r, e := matchingOutcomeOperation(c, id, expected, policy)
		if e == nil {
			if r.Selection.ConfiguredModelID != configuredModelID {
				return ErrConflict
			}
			if e = s.outcomeGuard(ctx, guard, r); e != nil {
				return e
			}
			out = r
			return errCatalogUnchanged
		}
		if !errors.Is(e, ErrNotFound) {
			return e
		}
		saved, e := matchingOutcomeIntent(c, id, configuredModelID, expected, policy)
		if e != nil {
			return e
		}
		if saved != claim {
			return ErrConflict
		}
		if checkpoint.Version != 0 {
			stored, e := lookupOutcomeSelection(c, expected.Key, id)
			if e != nil {
				return e
			}
			if !reflect.DeepEqual(stored, checkpoint) {
				return ErrConflict
			}
			if selectionGuard != nil {
				if e = s.outcomeSelectionGuard(ctx, selectionGuard, stored); e != nil {
					return e
				}
			}
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
		out.IntentID = id
		if checkpoint.Version != 0 {
			out.SelectionID = id
		}
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
		if e = validateOutcomeIntents(c); e != nil {
			return e
		}
		if e = validateOutcomeSelections(c); e != nil {
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
		if prior.Expected.Key == expected.Key {
			verified, e := lookupOutcomeOperation(c, expected.Key, prior.OperationID)
			if e != nil {
				return OutcomeRollbackReceipt{}, e
			}
			if verified.Expected.Revision == expected.Revision {
				return OutcomeRollbackReceipt{}, ErrConflict
			}
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
	candidate, err := outcomeRollbackCandidateForEntry(e, state)
	if err != nil {
		return err
	}
	if candidate.Predecessor != policy.Comparison.BaselineVersion {
		return ErrConflict
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
