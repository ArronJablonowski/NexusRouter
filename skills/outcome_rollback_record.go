package skills

import (
	"context"
	"encoding/json"
	"time"
)

// These are trusted, bounded, cancellation-cooperative host callbacks. The
// guard runs under the catalog lock and must not reenter the store. Reports
// remain advisory observations; separate operator policy authorizes this action.
type OutcomeSelector func(context.Context) (ComparisonSelectionReport, error)
type OutcomeRollbackGuard func(context.Context, OutcomeRollbackReceipt) error

type OutcomeRollbackReceipt struct {
	Version         int                       `json:"version"`
	OperationID     string                    `json:"operation_id"`
	Expected        ActivationState           `json:"expected"`
	Policy          ComparisonSelectionPolicy `json:"policy"`
	Selection       ComparisonSelectionReport `json:"selection"`
	After           ActivationState           `json:"after"`
	ActivationCount int                       `json:"activation_count"`
	CheckedAt       time.Time                 `json:"checked_at"`
	Decision        string                    `json:"decision"`
	IntentID        string                    `json:"intent_id,omitempty"`
}

func (r OutcomeRollbackReceipt) Validate() error {
	if r.IntentID != "" && r.IntentID != r.OperationID {
		return ErrInvalid
	}
	_, offset := r.CheckedAt.Zone()
	if r.Version != 1 || !identifier.MatchString(r.OperationID) || r.Expected.Validate() != nil || r.Expected.Active == "" || r.Policy.Validate() != nil || r.Policy.Comparison.Key != r.Expected.Key || r.Policy.Comparison.CandidateVersion != r.Expected.Active || r.Selection.Validate() != nil || r.Selection.Policy != r.Policy || r.After.Validate() != nil || r.After.Key != r.Expected.Key || r.ActivationCount < 2 || r.ActivationCount > 10000 || offset != 0 || r.CheckedAt.Year() < 1970 || r.CheckedAt.Year() >= 2261 {
		return ErrInvalid
	}
	signal := r.Selection.Comparison != nil && r.Selection.Comparison.Status == "regression_signal"
	if signal {
		if r.Decision != "rolled_back" || r.After.Active != r.Policy.Comparison.BaselineVersion || r.After.Revision == r.Expected.Revision || r.ActivationCount >= 10000 {
			return ErrInvalid
		}
	} else if r.Decision != "no_action" || r.After != r.Expected {
		return ErrInvalid
	}
	body, err := json.Marshal(r)
	if err != nil || len(body) > 64<<10 {
		return ErrInvalid
	}
	return nil
}

func validateOutcomeOperations(c *catalog) error {
	if len(c.OutcomeOperations) > 1000 || len(c.OutcomeOperations) > 0 && c.Schema < 6 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for id, r := range c.OutcomeOperations {
		if id != r.OperationID || r.Validate() != nil {
			return ErrInvalid
		}
		key := r.Expected.Key.index() + ":" + r.Expected.Revision
		if seen[key] {
			return ErrInvalid
		}
		seen[key] = true
		e, exists := c.Skills[r.Expected.Key.index()]
		if !exists || r.ActivationCount > len(e.Activations) || r.Decision == "rolled_back" && r.ActivationCount >= len(e.Activations) {
			return ErrInvalid
		}
		if r.Decision == "rolled_back" {
			a := e.Activations[r.ActivationCount]
			if a.OutcomeOperationID != id || !a.Rollback || a.From != r.Expected.Active || a.To != r.After.Active || !a.At.Equal(r.CheckedAt) {
				return ErrInvalid
			}
		}
	}
	for _, e := range c.Skills {
		for index, a := range e.Activations {
			if a.OutcomeOperationID != "" {
				r, ok := c.OutcomeOperations[a.OutcomeOperationID]
				if !ok || r.Expected.Key != e.Key || r.Decision != "rolled_back" || r.ActivationCount != index || a.From != r.Expected.Active || a.To != r.After.Active || !a.At.Equal(r.CheckedAt) {
					return ErrInvalid
				}
			}
		}
	}
	return nil
}

func lookupOutcomeOperation(c *catalog, key Key, id string) (OutcomeRollbackReceipt, error) {
	r, ok := c.OutcomeOperations[id]
	if !ok {
		return OutcomeRollbackReceipt{}, ErrNotFound
	}
	if r.Expected.Key != key {
		return OutcomeRollbackReceipt{}, ErrConflict
	}
	e, ok := c.Skills[key.index()]
	if !ok || r.Validate() != nil || r.ActivationCount > len(e.Activations) {
		return OutcomeRollbackReceipt{}, ErrInvalid
	}
	if r.IntentID != "" {
		intent, err := lookupOutcomeIntent(c, key, r.IntentID)
		if err != nil || !outcomeIntentReceiptMatches(intent, r) {
			return OutcomeRollbackReceipt{}, ErrInvalid
		}
	}
	prefix := e
	prefix.Activations = e.Activations[:r.ActivationCount]
	prefix.Active = prefix.Activations[len(prefix.Activations)-1].To
	before, err := stateForEntry(prefix)
	if err != nil || before != r.Expected || outcomeCandidate(prefix, r.Expected, r.Policy) != nil || outcomeDigests(prefix, r.Selection) != nil {
		return OutcomeRollbackReceipt{}, ErrInvalid
	}
	if r.Decision == "rolled_back" {
		if r.ActivationCount >= len(e.Activations) {
			return OutcomeRollbackReceipt{}, ErrInvalid
		}
		a := e.Activations[r.ActivationCount]
		if a.OutcomeOperationID != id || !a.Rollback || a.From != r.Expected.Active || a.To != r.After.Active || !a.At.Equal(r.CheckedAt) {
			return OutcomeRollbackReceipt{}, ErrInvalid
		}
		prefix.Activations = e.Activations[:r.ActivationCount+1]
		prefix.Active = a.To
	}
	after, err := stateForEntry(prefix)
	if err != nil || after != r.After {
		return OutcomeRollbackReceipt{}, ErrInvalid
	}
	return r, nil
}

// OutcomeRollbackOperation inspects a historical receipt without enabling
// automation, invoking callbacks, or changing the catalog.
func (s *FileStore) OutcomeRollbackOperation(ctx context.Context, key Key, id string) (OutcomeRollbackReceipt, error) {
	var out OutcomeRollbackReceipt
	if s == nil || ctx == nil || !s.permitted(key) || !identifier.MatchString(id) {
		return out, ErrInvalid
	}
	err := s.with(ctx, func(c *catalog) error { var err error; out, err = lookupOutcomeOperation(c, key, id); return err }, false)
	if err != nil {
		return OutcomeRollbackReceipt{}, err
	}
	return out, nil
}
