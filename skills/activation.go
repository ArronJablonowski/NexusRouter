package skills

import (
	"context"
	"time"
)

// Activate validates outside the storage lock, then compares the active version
// against expectedActive. This legacy check detects version changes, not an
// intervening activation followed by restoration; use ActivateAt for epoch CAS.
func (s *FileStore) Activate(ctx context.Context, key Key, id, expectedActive string, validator Validator, automatic bool) error {
	return s.activate(ctx, key, id, expectedActive, nil, validator, automatic)
}

// ActivateAt validates a candidate outside the storage lock, then atomically
// compares the complete activation revision. Returning to the same version
// after an intervening transition does not make a stale observation current.
func (s *FileStore) ActivateAt(ctx context.Context, expected ActivationState, id string, validator Validator, automatic bool) error {
	if ctx == nil || expected.Validate() != nil || !s.permitted(expected.Key) {
		return ErrInvalid
	}
	return s.activate(ctx, expected.Key, id, expected.Active, &expected, validator, automatic)
}

func (s *FileStore) activate(ctx context.Context, key Key, id, expectedActive string, expected *ActivationState, validator Validator, automatic bool) error {
	if !versionID(id) || (expectedActive != "" && !versionID(expectedActive)) {
		return ErrInvalid
	}
	if validator == nil {
		return ErrValidation
	}
	if automatic && !s.automatic.Load() {
		return ErrDisabled
	}
	v, err := s.Load(ctx, key, id)
	if err != nil {
		return err
	}
	evidence, err := validator.Validate(ctx, v)
	if err != nil {
		return ErrValidation
	}
	if !evidence.Passed || !evidence.Deterministic || !identifier.MatchString(evidence.ID) {
		return ErrValidation
	}
	return s.with(ctx, func(c *catalog) error {
		if automatic && !s.automatic.Load() {
			return ErrDisabled
		}
		e := c.Skills[key.index()]
		if expected != nil {
			current, err := stateForEntry(e)
			if err != nil {
				return err
			}
			if current != *expected {
				return ErrConflict
			}
		}
		if e.Active != expectedActive {
			return ErrConflict
		}
		if e.Active == id {
			return nil
		}
		if len(e.Activations) >= 10000 {
			return ErrInvalid
		}
		if e.Validated == nil {
			e.Validated = map[string]Evidence{}
		}
		e.Validated[id] = evidence
		e.Activations = append(e.Activations, activation{From: e.Active, To: id, At: time.Now().UTC()})
		e.Active = id
		c.Skills[key.index()] = e
		return nil
	}, true)
}

// Rollback reverses the latest activation not already undone. It cannot activate
// an unvalidated draft or undo the same historical activation twice.
// The legacy expectedActive check protects against changed versions, but not
// intervening transitions that restore that version. Use RollbackAt for epoch CAS.
func (s *FileStore) Rollback(ctx context.Context, key Key, expectedActive string, automatic bool) error {
	return s.rollback(ctx, key, expectedActive, nil, automatic)
}

// RollbackAt undoes one activation only if the observed activation revision is
// still current. The comparison and transition share the storage mutation lock.
func (s *FileStore) RollbackAt(ctx context.Context, expected ActivationState, automatic bool) error {
	if ctx == nil || expected.Validate() != nil || !s.permitted(expected.Key) {
		return ErrInvalid
	}
	return s.rollback(ctx, expected.Key, expected.Active, &expected, automatic)
}

func (s *FileStore) rollback(ctx context.Context, key Key, expectedActive string, expected *ActivationState, automatic bool) error {
	if !s.permitted(key) || !versionID(expectedActive) {
		return ErrInvalid
	}
	return s.with(ctx, func(c *catalog) error {
		if automatic && !s.automatic.Load() {
			return ErrDisabled
		}
		e, ok := c.Skills[key.index()]
		if !ok {
			return ErrNotFound
		}
		if expected != nil {
			current, err := stateForEntry(e)
			if err != nil {
				return err
			}
			if current != *expected {
				return ErrConflict
			}
		}
		if e.Active != expectedActive {
			return ErrConflict
		}
		if len(e.Activations) >= 10000 {
			return ErrInvalid
		}
		stack, err := activationStack(e)
		if err != nil {
			return err
		}
		if len(stack) == 0 || stack[len(stack)-1].From == "" {
			return ErrNotFound
		}
		previous := stack[len(stack)-1].From
		e.Activations = append(e.Activations, activation{From: e.Active, To: previous, At: time.Now().UTC(), Rollback: true})
		e.Active = previous
		c.Skills[key.index()] = e
		return nil
	}, true)
}
