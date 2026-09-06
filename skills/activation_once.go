package skills

import (
	"context"
	"errors"
	"time"
)

// ActivateOnce binds a trusted host operation to one validated activation.
// Exact retries acknowledge its historical receipt without rerunning validation
// or undoing a later rollback. Reusing an operation for another transition fails.
// The operation is not approval: the same policy and deterministic validator
// requirements as ActivateAt apply. Concurrent validators may both run outside
// the lock, so callbacks must be read-only, retry-safe and cooperative.
func (s *FileStore) ActivateOnce(ctx context.Context, operationID string, expected ActivationState, id string, validator Validator, automatic bool) error {
	if ctx == nil || s == nil || !identifier.MatchString(operationID) || expected.Validate() != nil || !s.permitted(expected.Key) || !versionID(id) {
		return ErrInvalid
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if s.readOnly || automatic && !s.automatic.Load() {
		return ErrDisabled
	}
	if validator == nil || nilRegressionValidator(validator) {
		return ErrValidation
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	complete := false
	err := s.with(ctx, func(c *catalog) error {
		if automatic && !s.automatic.Load() {
			return ErrDisabled
		}
		var err error
		complete, err = matchActivationOperation(c, operationID, expected, id)
		return err
	}, false)
	if err != nil || complete {
		return err
	}
	current, err := s.ActivationState(ctx, expected.Key)
	if err != nil {
		return err
	}
	if current != expected || current.Active == id {
		return s.activationOnceConcurrentResult(ctx, operationID, expected, id, automatic, ErrConflict)
	}
	candidate, err := s.Load(ctx, expected.Key, id)
	if err != nil {
		return err
	}
	if candidate.Validate() != nil || ctx.Err() != nil {
		return ErrValidation
	}
	evidence, err := regressionEvidence(ctx, validator, candidate)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil || !evidence.Passed || !evidence.Deterministic || !identifier.MatchString(evidence.ID) {
		return s.activationOnceConcurrentResult(ctx, operationID, expected, id, automatic, ErrValidation)
	}
	return s.with(ctx, func(c *catalog) error {
		if automatic && !s.automatic.Load() {
			return ErrDisabled
		}
		complete, err := matchActivationOperation(c, operationID, expected, id)
		if err != nil {
			return err
		}
		if complete {
			return errCatalogUnchanged
		}
		e, exists := c.Skills[expected.Key.index()]
		if !exists {
			return ErrNotFound
		}
		state, err := stateForEntry(e)
		if err != nil {
			return err
		}
		if state != expected || state.Active == id {
			return ErrConflict
		}
		if len(e.Activations) >= 10000 {
			return ErrInvalid
		}
		if e.Validated == nil {
			e.Validated = map[string]Evidence{}
		}
		e.Validated[id] = evidence
		proof := evidence
		e.Activations = append(e.Activations, activation{From: expected.Active, To: id, At: time.Now().UTC(), OperationID: operationID, BeforeRevision: expected.Revision, Evidence: &proof})
		e.Active = id
		c.Skills[expected.Key.index()] = e
		c.Schema = 3
		return nil
	}, true)
}

// Another caller can commit the same operation between preflight and validation.
// A matching durable receipt acknowledges that operation, not this callback's
// outcome. A different binding or an absent receipt never suppresses failure.
func (s *FileStore) activationOnceConcurrentResult(ctx context.Context, operationID string, expected ActivationState, id string, automatic bool, original error) error {
	complete := false
	err := s.with(ctx, func(c *catalog) error {
		if automatic && !s.automatic.Load() {
			return ErrDisabled
		}
		var err error
		complete, err = matchActivationOperation(c, operationID, expected, id)
		return err
	}, false)
	if err != nil {
		return err
	}
	if complete {
		return nil
	}
	return original
}

func matchActivationOperation(c *catalog, operationID string, expected ActivationState, id string) (bool, error) {
	receipt, err := lookupActivationOperation(c, expected.Key, operationID)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if receipt.Expected != expected || receipt.Candidate != id {
		return false, ErrConflict
	}
	return true, nil
}
