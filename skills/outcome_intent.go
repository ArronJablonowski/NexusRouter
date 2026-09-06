package skills

import (
	"context"
	"errors"
	"time"
)

// ErrOutcomePending means a single selector opportunity was durably claimed
// without a completed receipt. It does not distinguish live execution from a
// crash; retry must not select again or replace the policy/operation binding.
var ErrOutcomePending = errors.New("outcome selection intent unresolved")

type OutcomeRollbackIntent struct {
	Version           int                       `json:"version"`
	OperationID       string                    `json:"operation_id"`
	ConfiguredModelID string                    `json:"configured_model_id,omitempty"`
	Expected          ActivationState           `json:"expected"`
	Policy            ComparisonSelectionPolicy `json:"policy"`
	ActivationCount   int                       `json:"activation_count"`
	PreparedAt        time.Time                 `json:"prepared_at"`
}

// The trusted guard receives immutable metadata before persistence, under the
// catalog lock. It must be bounded, cancellation-cooperative and nonreentrant.
type OutcomeIntentGuard func(context.Context, OutcomeRollbackIntent) error

func (i OutcomeRollbackIntent) Validate() error {
	_, offset := i.PreparedAt.Zone()
	if i.Version != 1 || !identifier.MatchString(i.OperationID) || i.ConfiguredModelID != "" && !identifier.MatchString(i.ConfiguredModelID) || i.Expected.Validate() != nil || i.Expected.Active == "" || i.Policy.Validate() != nil || i.Policy.Comparison.Key != i.Expected.Key || i.Policy.Comparison.CandidateVersion != i.Expected.Active || i.ActivationCount < 2 || i.ActivationCount > 10000 || i.PreparedAt.Year() < 1970 || i.PreparedAt.Year() >= 2261 || offset != 0 {
		return ErrInvalid
	}
	return nil
}

func outcomeIntentReceiptMatches(i OutcomeRollbackIntent, r OutcomeRollbackReceipt) bool {
	return r.IntentID == i.OperationID && r.OperationID == i.OperationID && r.Expected == i.Expected && r.Policy == i.Policy && r.Selection.ConfiguredModelID == i.ConfiguredModelID && r.ActivationCount == i.ActivationCount && !r.CheckedAt.Before(i.PreparedAt)
}

func validateOutcomeIntents(c *catalog) error {
	if len(c.OutcomeIntents) > 1000 || len(c.OutcomeIntents) > 0 && c.Schema < 7 {
		return ErrInvalid
	}
	seen := map[string]string{}
	for id, r := range c.OutcomeOperations {
		seen[r.Expected.Key.index()+":"+r.Expected.Revision] = id
		if r.IntentID != "" {
			i, ok := c.OutcomeIntents[r.IntentID]
			if !ok || !outcomeIntentReceiptMatches(i, r) {
				return ErrInvalid
			}
		}
	}
	for id, i := range c.OutcomeIntents {
		if id != i.OperationID || i.Validate() != nil {
			return ErrInvalid
		}
		e, ok := c.Skills[i.Expected.Key.index()]
		if !ok || i.ActivationCount > len(e.Activations) {
			return ErrInvalid
		}
		key := i.Expected.Key.index() + ":" + i.Expected.Revision
		if prior := seen[key]; prior != "" && prior != id {
			return ErrInvalid
		}
		seen[key] = id
		if r, ok := c.OutcomeOperations[id]; ok && !outcomeIntentReceiptMatches(i, r) {
			return ErrInvalid
		}
	}
	if len(seen) > 1000 {
		return ErrInvalid
	}
	return nil
}

func lookupOutcomeIntent(c *catalog, key Key, id string) (OutcomeRollbackIntent, error) {
	i, ok := c.OutcomeIntents[id]
	if !ok {
		return OutcomeRollbackIntent{}, ErrNotFound
	}
	if i.Expected.Key != key {
		return OutcomeRollbackIntent{}, ErrConflict
	}
	e, ok := c.Skills[key.index()]
	if !ok || i.Validate() != nil || i.ActivationCount > len(e.Activations) {
		return OutcomeRollbackIntent{}, ErrInvalid
	}
	e.Activations = e.Activations[:i.ActivationCount]
	e.Active = e.Activations[len(e.Activations)-1].To
	if outcomeCandidate(e, i.Expected, i.Policy) != nil {
		return OutcomeRollbackIntent{}, ErrInvalid
	}
	return i, nil
}

func matchingOutcomeIntent(c *catalog, id, model string, expected ActivationState, policy ComparisonSelectionPolicy) (OutcomeRollbackIntent, error) {
	i, err := lookupOutcomeIntent(c, expected.Key, id)
	if err == nil {
		if i.Expected != expected || i.Policy != policy || i.ConfiguredModelID != model {
			return OutcomeRollbackIntent{}, ErrConflict
		}
		return i, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return OutcomeRollbackIntent{}, err
	}
	for _, prior := range c.OutcomeIntents {
		if prior.Expected.Key == expected.Key {
			verified, e := lookupOutcomeIntent(c, expected.Key, prior.OperationID)
			if e != nil {
				return OutcomeRollbackIntent{}, e
			}
			if verified.Expected.Revision == expected.Revision {
				return OutcomeRollbackIntent{}, ErrConflict
			}
		}
	}
	return OutcomeRollbackIntent{}, ErrNotFound
}

// Admission alone checks all historical bindings: a corrupt claim moved to a
// different key must not free the original revision. Ordinary catalog reads
// remain structural, and exact receipt/intent lookup verifies only that record.
func validateOutcomeAdmission(ctx context.Context, c *catalog) error {
	for id, i := range c.OutcomeIntents {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := lookupOutcomeIntent(c, i.Expected.Key, id); err != nil {
			return err
		}
	}
	for id, r := range c.OutcomeOperations {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := lookupOutcomeOperation(c, r.Expected.Key, id); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// OutcomeRollbackIntent reads the immutable claim. A claim alone is not proof
// of selector dispatch, completion or failure; inspect the receipt separately.
func (s *FileStore) OutcomeRollbackIntent(ctx context.Context, key Key, id string) (OutcomeRollbackIntent, error) {
	var out OutcomeRollbackIntent
	if s == nil || ctx == nil || !s.permitted(key) || !identifier.MatchString(id) {
		return out, ErrInvalid
	}
	err := s.with(ctx, func(c *catalog) error { var err error; out, err = lookupOutcomeIntent(c, key, id); return err }, false)
	if err != nil {
		return OutcomeRollbackIntent{}, err
	}
	return out, nil
}
