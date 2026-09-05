package skills

import (
	"context"
	"time"
)

// Activate validates outside the storage lock, then compares the active version
// against expectedActive. Concurrent activation cannot silently overwrite it.
func (s *FileStore) Activate(ctx context.Context, key Key, id, expectedActive string, validator Validator, automatic bool) error {
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
// expectedActive protects against a stale regression detector.
func (s *FileStore) Rollback(ctx context.Context, key Key, expectedActive string, automatic bool) error {
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
