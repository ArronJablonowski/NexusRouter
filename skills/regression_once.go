package skills

import (
	"context"
	"errors"
	"time"
)

// RevalidateAndRollbackOnce commits a passing check or a failed check and its
// rollback atomically. Exact retries acknowledge the historical result, even
// after later activation changes. Concurrent callbacks can both run; validators
// must remain trusted, read-only, retry-safe and cancellation-cooperative.
func (s *FileStore) RevalidateAndRollbackOnce(ctx context.Context, operationID, validatorID string, expected ActivationState, validator Validator) (RegressionOperation, error) {
	return s.revalidateAndRollbackOnce(ctx, operationID, validatorID, expected, validator, nil)
}

func (s *FileStore) revalidateAndRollbackOnce(ctx context.Context, operationID, validatorID string, expected ActivationState, validator Validator, monitor *regressionMonitorExecution) (RegressionOperation, error) {
	zero := RegressionOperation{}
	if s == nil || ctx == nil || !identifier.MatchString(operationID) || !identifier.MatchString(validatorID) || expected.Validate() != nil || expected.Active == "" || !s.permitted(expected.Key) {
		return zero, ErrInvalid
	}
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	if s.readOnly || !s.automatic.Load() {
		return zero, ErrDisabled
	}
	if validator == nil || nilRegressionValidator(validator) {
		return zero, ErrValidation
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if o, err := s.matchRegressionOperation(ctx, operationID, validatorID, expected, monitor); !errors.Is(err, ErrNotFound) {
		return o, err
	}
	current, err := s.ActivationState(ctx, expected.Key)
	if err != nil {
		return zero, err
	}
	if current != expected {
		if monitor != nil {
			monitor.failureCode = "stale_activation"
		}
		return s.regressionConcurrentResult(ctx, operationID, validatorID, expected, ErrConflict, monitor)
	}
	v, err := s.Load(ctx, expected.Key, expected.Active)
	if err != nil {
		return zero, err
	}
	if v.Validate() != nil {
		return zero, ErrValidation
	}
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	proof, err := regressionEvidence(ctx, validator, v)
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	if err != nil || !proof.Deterministic || !identifier.MatchString(proof.ID) {
		if monitor != nil {
			monitor.failureCode = "check_failed"
		}
		return s.regressionConcurrentResult(ctx, operationID, validatorID, expected, ErrValidation, monitor)
	}
	var out RegressionOperation
	err = s.with(ctx, func(c *catalog) error {
		if !s.automatic.Load() {
			return ErrDisabled
		}
		if err := regressionMonitorFence(ctx, c, operationID, monitor); err != nil {
			return err
		}
		existing, err := matchingRegressionOperation(c, operationID, validatorID, expected)
		if err == nil {
			out = existing
			return errCatalogUnchanged
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		if len(c.RegressionOperations) >= 1000 {
			return ErrInvalid
		}
		e, exists := c.Skills[expected.Key.index()]
		if !exists {
			return ErrNotFound
		}
		state, err := stateForEntry(e)
		if err != nil {
			return err
		}
		if state != expected {
			if monitor != nil {
				monitor.failureCode = "stale_activation"
			}
			return ErrConflict
		}
		out = RegressionOperation{Version: 1, OperationID: operationID, ValidatorID: validatorID, Expected: expected, Result: RegressionResult{State: expected, Evidence: proof, RolledBack: !proof.Passed}, After: expected, ActivationCount: len(e.Activations), CheckedAt: time.Now().UTC()}
		if !proof.Passed {
			if len(e.Activations) >= 10000 {
				return ErrInvalid
			}
			stack, err := activationStack(e)
			if err != nil {
				return err
			}
			if len(stack) == 0 || stack[len(stack)-1].From == "" {
				if monitor != nil {
					monitor.failureCode = "check_failed"
				}
				return ErrNotFound
			}
			previous := stack[len(stack)-1].From
			copy := proof
			e.Activations = append(e.Activations, activation{From: e.Active, To: previous, At: out.CheckedAt, Rollback: true, Regression: &copy})
			e.Active = previous
			out.After, err = stateForEntry(e)
			if err != nil {
				return err
			}
			c.Skills[expected.Key.index()] = e
		}
		if out.Validate() != nil {
			return ErrInvalid
		}
		if c.RegressionOperations == nil {
			c.RegressionOperations = map[string]RegressionOperation{}
		}
		c.RegressionOperations[operationID] = out
		if c.Schema < 4 {
			c.Schema = 4
		}
		return nil
	}, true)
	if err != nil {
		return zero, err
	}
	return out, nil
}

func matchingRegressionOperation(c *catalog, operationID, validatorID string, expected ActivationState) (RegressionOperation, error) {
	o, err := lookupRegressionOperation(c, expected.Key, operationID)
	if err != nil {
		return RegressionOperation{}, err
	}
	if o.Expected != expected || o.ValidatorID != validatorID {
		return RegressionOperation{}, ErrConflict
	}
	return o, nil
}

func (s *FileStore) matchRegressionOperation(ctx context.Context, operationID, validatorID string, expected ActivationState, monitor *regressionMonitorExecution) (RegressionOperation, error) {
	var out RegressionOperation
	err := s.with(ctx, func(c *catalog) error {
		if !s.automatic.Load() {
			return ErrDisabled
		}
		if err := regressionMonitorFence(ctx, c, operationID, monitor); err != nil {
			return err
		}
		var err error
		out, err = matchingRegressionOperation(c, operationID, validatorID, expected)
		if errors.Is(err, ErrNotFound) && len(c.RegressionOperations) >= 1000 {
			return ErrInvalid
		}
		return err
	}, false)
	if err != nil {
		return RegressionOperation{}, err
	}
	return out, nil
}

func (s *FileStore) regressionConcurrentResult(ctx context.Context, operationID, validatorID string, expected ActivationState, original error, monitor *regressionMonitorExecution) (RegressionOperation, error) {
	o, err := s.matchRegressionOperation(ctx, operationID, validatorID, expected, monitor)
	if errors.Is(err, ErrNotFound) {
		return RegressionOperation{}, original
	}
	return o, err
}
