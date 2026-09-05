package skills

import (
	"context"
	"reflect"
	"time"
)

// RegressionResult identifies the observed activation checked by a validator.
// State remains that observation even when RolledBack is true; callers can read
// ActivationState again to obtain the newly current revision. Passing checks do
// not add activation history or establish correctness outside the validator.
type RegressionResult struct {
	State      ActivationState `json:"state"`
	Evidence   Evidence        `json:"evidence"`
	RolledBack bool            `json:"rolled_back"`
}

// RevalidateAndRollback checks one pinned active version and automatically
// restores its predecessor only on valid deterministic failure evidence. Both
// passing and failed results are fenced against intervening activation changes
// and the automatic-update kill switch. Failure evidence and a rollback commit
// atomically; validation itself runs outside the storage lock.
//
// Validators are trusted host code, not sandboxed tools or model judgments.
// They must honor cancellation and support concurrent calls. The three-second
// deadline is cooperative: this method joins the callback rather than abandoning
// a goroutine if it refuses to stop. No raw callback error or panic is returned.
func (s *FileStore) RevalidateAndRollback(ctx context.Context, expected ActivationState, validator Validator) (RegressionResult, error) {
	if ctx == nil || s == nil || expected.Validate() != nil || expected.Active == "" || !s.permitted(expected.Key) {
		return RegressionResult{}, ErrInvalid
	}
	if ctx.Err() != nil {
		return RegressionResult{}, ctx.Err()
	}
	if s.readOnly || !s.automatic.Load() {
		return RegressionResult{}, ErrDisabled
	}
	if validator == nil || nilRegressionValidator(validator) {
		return RegressionResult{}, ErrValidation
	}
	bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	current, err := s.ActivationState(bounded, expected.Key)
	if err != nil {
		return RegressionResult{}, err
	}
	if current != expected {
		return RegressionResult{}, ErrConflict
	}
	version, err := s.Load(bounded, expected.Key, expected.Active)
	if err != nil {
		return RegressionResult{}, err
	}
	if version.Validate() != nil {
		return RegressionResult{}, ErrValidation
	}
	if bounded.Err() != nil {
		return RegressionResult{}, bounded.Err()
	}
	proof, err := regressionEvidence(bounded, validator, version)
	if bounded.Err() != nil {
		return RegressionResult{}, bounded.Err()
	}
	if err != nil || !proof.Deterministic || !identifier.MatchString(proof.ID) {
		return RegressionResult{}, ErrValidation
	}
	if !proof.Passed {
		if err := s.rollback(bounded, expected.Key, expected.Active, &expected, true, &proof); err != nil {
			return RegressionResult{}, err
		}
		return RegressionResult{State: expected, Evidence: proof, RolledBack: true}, nil
	}
	err = s.with(bounded, func(c *catalog) error {
		if !s.automatic.Load() {
			return ErrDisabled
		}
		entry, ok := c.Skills[expected.Key.index()]
		if !ok {
			return ErrNotFound
		}
		state, err := stateForEntry(entry)
		if err != nil {
			return err
		}
		if state != expected {
			return ErrConflict
		}
		return nil
	}, false)
	if err != nil {
		return RegressionResult{}, err
	}
	return RegressionResult{State: expected, Evidence: proof}, nil
}

func nilRegressionValidator(v Validator) bool {
	value := reflect.ValueOf(v)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	}
	return false
}

func regressionEvidence(ctx context.Context, validator Validator, version Version) (proof Evidence, err error) {
	defer func() {
		if recover() != nil {
			proof, err = Evidence{}, ErrValidation
		}
	}()
	proof, err = validator.Validate(ctx, version)
	if err != nil {
		return Evidence{}, ErrValidation
	}
	return proof, nil
}
