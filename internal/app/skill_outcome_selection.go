package app

import (
	"context"
	"time"

	"github.com/ArronJablonowski/NexusRouter/skills"
)

// OutcomeSelectionCheckpoint inspects saved evidence, not a completion receipt.
// Inspection neither reads newer feedback nor authorizes an activation change.
func (s *Service) OutcomeSelectionCheckpoint(ctx context.Context, key skills.Key, operation string) (out skills.OutcomeSelectionCheckpoint, err error) {
	defer func() {
		if recover() != nil {
			out, err = skills.OutcomeSelectionCheckpoint{}, ErrAdmission
		}
	}()
	if ctx == nil || ctx.Err() != nil || !s.skillActivationConfigured(key) || !skillGenerationIdentifier.MatchString(operation) {
		return out, ErrAdmission
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	root := s.settings.Skills.Root
	secrets := memorySecrets(s.settings, s.secret)
	clean := func(value any) bool {
		return s.settings.Skills.Root == root && s.skillActivationConfigured(key) && selectionValueClean([]any{key, operation, value}, secrets)
	}
	if !clean(nil) {
		return out, ErrAdmission
	}
	store, err := skills.OpenReadOnly(root, []string{key.Scope})
	if err != nil {
		return out, ErrAdmission
	}
	defer store.Close()
	checkpoint, err := store.OutcomeSelectionCheckpoint(ctx, key, operation)
	secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
	if err != nil || ctx.Err() != nil || checkpoint.Validate() != nil || checkpoint.Intent.Expected.Key != key || checkpoint.OperationID != operation || !clean(checkpoint) {
		return out, ErrAdmission
	}
	return checkpoint, nil
}
