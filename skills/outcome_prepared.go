package skills

import (
	"context"
	"errors"
	"time"
)

// OutcomeRollbackPrepared atomically claims an activation revision and stores
// its already-selected evidence before attempting the final adjudication. It is
// intended for supervisors which first wait, read-only, for a decision-ready
// window. A crash after the atomic prepare can resume from the fixed checkpoint
// without selecting newer evidence.
func (s *FileStore) OutcomeRollbackPrepared(ctx context.Context, id, configuredModelID string,
	expected ActivationState, policy ComparisonSelectionPolicy, selection ComparisonSelectionReport,
	guard OutcomeRollbackGuard, intentGuard OutcomeIntentGuard, selectionGuard OutcomeSelectionGuard,
) (out OutcomeRollbackReceipt, err error) {
	defer func() {
		if recover() != nil {
			out, err = OutcomeRollbackReceipt{}, ErrInvalid
		}
	}()
	if s == nil || ctx == nil || !s.permitted(expected.Key) || !identifier.MatchString(id) ||
		(configuredModelID != "" && !identifier.MatchString(configuredModelID)) || expected.Validate() != nil ||
		policy.Validate() != nil || policy.Comparison.Key != expected.Key || policy.Comparison.CandidateVersion != expected.Active ||
		selection.Validate() != nil || selection.Policy != policy || selection.ConfiguredModelID != configuredModelID ||
		guard == nil || intentGuard == nil || selectionGuard == nil {
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

	prepared := false
	err = s.with(ctx, func(c *catalog) error {
		if !s.automatic.Load() || !s.outcomeRollback.Load() {
			return ErrDisabled
		}
		if receipt, e := matchingOutcomeOperation(c, id, expected, policy); e == nil {
			if receipt.Selection.ConfiguredModelID != configuredModelID {
				return ErrConflict
			}
			if e = s.outcomeGuard(ctx, guard, receipt); e != nil {
				return e
			}
			out = receipt
			return errCatalogUnchanged
		} else if !errors.Is(e, ErrNotFound) {
			return e
		}
		if claim, e := matchingOutcomeIntent(c, id, configuredModelID, expected, policy); e == nil {
			checkpoint, lookupErr := lookupOutcomeSelection(c, expected.Key, id)
			if lookupErr != nil {
				return ErrOutcomePending
			}
			if claim != checkpoint.Intent || checkpoint.Report.Validate() != nil || checkpoint.Report.Policy != policy ||
				checkpoint.Report.ConfiguredModelID != configuredModelID {
				return ErrConflict
			}
			prepared = true
			return errCatalogUnchanged
		} else if !errors.Is(e, ErrNotFound) {
			return e
		}
		if len(c.OutcomeOperations) >= 1000 || len(c.OutcomeIntents) >= 1000 || len(c.OutcomeSelections) >= 1000 {
			return ErrInvalid
		}
		if e := validateOutcomeAdmission(ctx, c); e != nil {
			return e
		}
		entry, ok := c.Skills[expected.Key.index()]
		if !ok {
			return ErrNotFound
		}
		if e := outcomeCandidate(entry, expected, policy); e != nil {
			return e
		}
		if e := outcomeDigests(entry, selection); e != nil {
			return e
		}
		now := time.Now().UTC()
		claim := OutcomeRollbackIntent{Version: 1, OperationID: id, ConfiguredModelID: configuredModelID, Expected: expected,
			Policy: policy, ActivationCount: len(entry.Activations), PreparedAt: now}
		checkpoint := OutcomeSelectionCheckpoint{Version: 1, OperationID: id, Intent: claim, Report: selection, SelectedAt: now}
		if claim.Validate() != nil || checkpoint.Validate() != nil {
			return ErrInvalid
		}
		if e := intentGuard(ctx, claim); e != nil {
			return e
		}
		if e := s.outcomeSelectionGuard(ctx, selectionGuard, checkpoint); e != nil {
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
		if c.OutcomeSelections == nil {
			c.OutcomeSelections = map[string]OutcomeSelectionCheckpoint{}
		}
		c.OutcomeIntents[id] = claim
		c.OutcomeSelections[id] = checkpoint
		if c.Schema < 8 {
			c.Schema = 8
		}
		if e := validateOutcomeIntents(c); e != nil {
			return e
		}
		if e := validateOutcomeSelections(c); e != nil {
			return e
		}
		prepared = true
		return nil
	}, true)
	if err != nil {
		return OutcomeRollbackReceipt{}, err
	}
	if out.Version != 0 {
		return out, nil
	}
	if !prepared {
		return OutcomeRollbackReceipt{}, ErrInvalid
	}
	return s.outcomeRollbackOnce(ctx, id, configuredModelID, expected, policy,
		func(context.Context) (ComparisonSelectionReport, error) {
			return ComparisonSelectionReport{}, ErrInvalid
		},
		guard, intentGuard, selectionGuard)
}
