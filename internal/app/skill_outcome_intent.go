package app

import (
	"context"
	"time"

	"github.com/ArronJablonowski/NexusRouter/skills"
)

// OutcomeRollbackIntent inspects the immutable preselection attempt without
// creating a catalog, reading outcome evidence, or authorizing retry. An intent
// without a corresponding receipt has an unknown or failed result, not success.
func (s *Service) OutcomeRollbackIntent(ctx context.Context, key skills.Key, operation string) (out skills.OutcomeRollbackIntent, err error) {
	defer func() {
		if recover() != nil {
			out, err = skills.OutcomeRollbackIntent{}, ErrAdmission
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
	intent, err := store.OutcomeRollbackIntent(ctx, key, operation)
	secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
	if err != nil || ctx.Err() != nil || intent.Validate() != nil || intent.Expected.Key != key || intent.OperationID != operation || !clean(intent) {
		return out, ErrAdmission
	}
	return intent, nil
}
