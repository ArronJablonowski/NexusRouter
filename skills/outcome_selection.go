package skills

import (
	"context"
	"encoding/json"
	"reflect"
	"time"
)

// A checkpoint records selected evidence, not completed adjudication. It can be
// resumed without selecting newer feedback, subject to current guards and CAS.
type OutcomeSelectionCheckpoint struct {
	Version     int                       `json:"version"`
	OperationID string                    `json:"operation_id"`
	Intent      OutcomeRollbackIntent     `json:"intent"`
	Report      ComparisonSelectionReport `json:"report"`
	SelectedAt  time.Time                 `json:"selected_at"`
}

// Trusted, nonreentrant, bounded guard executed under the catalog write lock
// before selected metadata is persisted. The argument is detached from storage.
type OutcomeSelectionGuard func(context.Context, OutcomeSelectionCheckpoint) error

func (c OutcomeSelectionCheckpoint) Validate() error {
	_, offset := c.SelectedAt.Zone()
	if c.Version != 1 || c.Intent.Validate() != nil || c.OperationID != c.Intent.OperationID || c.Report.Validate() != nil || c.Report.Policy != c.Intent.Policy || c.Report.ConfiguredModelID != c.Intent.ConfiguredModelID || offset != 0 || c.SelectedAt.Year() < 1970 || c.SelectedAt.Year() >= 2261 || c.SelectedAt.Before(c.Intent.PreparedAt) {
		return ErrInvalid
	}
	body, err := json.Marshal(c)
	if err != nil || len(body) > 64<<10 {
		return ErrInvalid
	}
	return nil
}

func outcomeSelectionReceiptMatches(c OutcomeSelectionCheckpoint, r OutcomeRollbackReceipt) bool {
	return r.SelectionID == c.OperationID && outcomeIntentReceiptMatches(c.Intent, r) && !r.CheckedAt.Before(c.SelectedAt) && reflect.DeepEqual(r.Selection, c.Report)
}

func validateOutcomeSelections(c *catalog) error {
	if len(c.OutcomeSelections) > 1000 || len(c.OutcomeSelections) > 0 && c.Schema < 8 {
		return ErrInvalid
	}
	for id, s := range c.OutcomeSelections {
		if id != s.OperationID || s.Validate() != nil {
			return ErrInvalid
		}
		i, ok := c.OutcomeIntents[id]
		if !ok || i != s.Intent {
			return ErrInvalid
		}
		if r, ok := c.OutcomeOperations[id]; ok && !outcomeSelectionReceiptMatches(s, r) {
			return ErrInvalid
		}
	}
	for _, r := range c.OutcomeOperations {
		if r.SelectionID != "" {
			s, ok := c.OutcomeSelections[r.SelectionID]
			if !ok || !outcomeSelectionReceiptMatches(s, r) {
				return ErrInvalid
			}
		}
	}
	return nil
}

func lookupOutcomeSelection(c *catalog, key Key, id string) (OutcomeSelectionCheckpoint, error) {
	s, ok := c.OutcomeSelections[id]
	if !ok {
		return OutcomeSelectionCheckpoint{}, ErrNotFound
	}
	if s.Intent.Expected.Key != key {
		return OutcomeSelectionCheckpoint{}, ErrConflict
	}
	i, err := lookupOutcomeIntent(c, key, id)
	if err != nil || s.Validate() != nil || i != s.Intent {
		return OutcomeSelectionCheckpoint{}, ErrInvalid
	}
	if outcomeDigests(c.Skills[key.index()], s.Report) != nil {
		return OutcomeSelectionCheckpoint{}, ErrInvalid
	}
	return s, nil
}

func (s *FileStore) OutcomeSelectionCheckpoint(ctx context.Context, key Key, id string) (OutcomeSelectionCheckpoint, error) {
	var out OutcomeSelectionCheckpoint
	if s == nil || ctx == nil || !s.permitted(key) || !identifier.MatchString(id) {
		return out, ErrInvalid
	}
	err := s.with(ctx, func(c *catalog) error { var err error; out, err = lookupOutcomeSelection(c, key, id); return err }, false)
	if err != nil {
		return OutcomeSelectionCheckpoint{}, err
	}
	return out, nil
}

func (s *FileStore) outcomeSelectionGuard(ctx context.Context, guard OutcomeSelectionGuard, checkpoint OutcomeSelectionCheckpoint) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrInvalid
		}
	}()
	if checkpoint.Validate() != nil {
		return ErrInvalid
	}
	if !s.automatic.Load() || !s.outcomeRollback.Load() {
		return ErrDisabled
	}
	body, err := json.Marshal(checkpoint)
	if err != nil {
		return ErrInvalid
	}
	var owned OutcomeSelectionCheckpoint
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

func (s *FileStore) saveOutcomeSelection(ctx context.Context, claim OutcomeRollbackIntent, report ComparisonSelectionReport, guard OutcomeSelectionGuard) (out OutcomeSelectionCheckpoint, err error) {
	err = s.with(ctx, func(c *catalog) error {
		if !s.automatic.Load() || !s.outcomeRollback.Load() {
			return ErrDisabled
		}
		i, e := matchingOutcomeIntent(c, claim.OperationID, claim.ConfiguredModelID, claim.Expected, claim.Policy)
		if e != nil {
			return e
		}
		if i != claim {
			return ErrConflict
		}
		if _, ok := c.OutcomeSelections[claim.OperationID]; ok {
			return ErrConflict
		}
		if _, ok := c.OutcomeOperations[claim.OperationID]; ok {
			return ErrConflict
		}
		if len(c.OutcomeSelections) >= 1000 {
			return ErrInvalid
		}
		entry, ok := c.Skills[claim.Expected.Key.index()]
		if !ok {
			return ErrNotFound
		}
		if e = outcomeCandidate(entry, claim.Expected, claim.Policy); e != nil {
			return e
		}
		if e = outcomeDigests(entry, report); e != nil {
			return e
		}
		out = OutcomeSelectionCheckpoint{Version: 1, OperationID: claim.OperationID, Intent: claim, Report: report, SelectedAt: time.Now().UTC()}
		if e = s.outcomeSelectionGuard(ctx, guard, out); e != nil {
			return e
		}
		if c.OutcomeSelections == nil {
			c.OutcomeSelections = map[string]OutcomeSelectionCheckpoint{}
		}
		c.OutcomeSelections[out.OperationID] = out
		if c.Schema < 8 {
			c.Schema = 8
		}
		return validateOutcomeSelections(c)
	}, true)
	if err != nil {
		return OutcomeSelectionCheckpoint{}, err
	}
	return out, nil
}
