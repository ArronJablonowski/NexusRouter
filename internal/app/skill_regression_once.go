package app

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/ArronJablonowski/NexusRouter/skills"
)

// SkillRegressionOperation reads a historical check receipt without creating
// storage or invoking validation. Its After state is not necessarily current.
func (s *Service) SkillRegressionOperation(ctx context.Context, key skills.Key, operation string) (skills.RegressionOperation, error) {
	zero := skills.RegressionOperation{}
	if ctx == nil || !s.skillActivationConfigured(key) || !skillGenerationIdentifier.MatchString(operation) {
		return zero, ErrAdmission
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	secrets := memorySecrets(s.settings, s.secret)
	if ctx.Err() != nil || !skillActivationClean(key, []string{key.Scope, key.Name, operation}, secrets) {
		return zero, ErrAdmission
	}
	store, err := skills.OpenReadOnly(s.settings.Skills.Root, []string{key.Scope})
	if err != nil {
		return zero, ErrAdmission
	}
	defer store.Close()
	receipt, err := store.RegressionOperation(ctx, key, operation)
	secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
	if err != nil || receipt.Validate() != nil || receipt.OperationID != operation || receipt.Expected.Key != key || ctx.Err() != nil || !skillRegressionOperationClean(receipt, secrets) {
		return zero, ErrAdmission
	}
	return receipt, nil
}

// RevalidateSkillVersionOnce binds an explicit operation and validator policy
// identity to a durable deterministic check. Exact receipt retries do not invoke
// validation or roll back again. A new operation still needs the same trusted,
// read-only, retry-safe callback and configured rollback policy as revalidation.
func (s *Service) RevalidateSkillVersionOnce(ctx context.Context, operation, validatorID string, expected skills.ActivationState, validator skills.Validator) (skills.RegressionOperation, error) {
	zero := skills.RegressionOperation{}
	bad := func() (skills.RegressionOperation, error) { return zero, ErrAdmission }
	if ctx == nil || !s.skillActivationConfigured(expected.Key) || !s.settings.Skills.Rollback || !skillGenerationIdentifier.MatchString(operation) || !skillGenerationIdentifier.MatchString(validatorID) || expected.Validate() != nil || !skillActivationID(expected.Active) || validator == nil || nilSkillActivationValidator(validator) {
		return bad()
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	secrets := memorySecrets(s.settings, s.secret)
	identities := []string{operation, validatorID, expected.Key.Scope, expected.Key.Name, expected.Active, expected.Revision}
	if ctx.Err() != nil || !skillActivationClean(expected, identities, secrets) {
		return bad()
	}
	read, err := skills.OpenReadOnly(s.settings.Skills.Root, []string{expected.Key.Scope})
	if err != nil {
		return bad()
	}
	defer read.Close()
	receipt, err := read.RegressionOperation(ctx, expected.Key, operation)
	if err == nil {
		observed := append(secrets, memorySecrets(s.settings, s.secret)...)
		if receipt.Validate() != nil || receipt.OperationID != operation || receipt.ValidatorID != validatorID || receipt.Expected != expected || ctx.Err() != nil || !skillRegressionOperationClean(receipt, observed) {
			return bad()
		}
		return receipt, nil
	}
	if !errors.Is(err, skills.ErrNotFound) {
		return bad()
	}
	current, err := read.ActivationState(ctx, expected.Key)
	if err != nil || current != expected {
		return bad()
	}
	candidate, err := read.Load(ctx, expected.Key, expected.Active)
	if err != nil || candidate.Validate() != nil || !skillActivationCandidateClean(candidate, secrets) {
		return bad()
	}
	body, err := json.Marshal(candidate)
	if err != nil || ctx.Err() != nil {
		return bad()
	}
	store, err := skills.Open(s.settings.Skills.Root, []string{expected.Key.Scope})
	if err != nil {
		return bad()
	}
	defer store.Close()
	store.SetAutomatic(true)
	guarded := s.guardSkillValidator(expected, candidate, body, secrets, validator)
	operationGuard := skills.ValidatorFunc(func(call context.Context, version skills.Version) (skills.Evidence, error) {
		observed := append(append([]string(nil), secrets...), memorySecrets(s.settings, s.secret)...)
		if !skillActivationClean(expected, identities, observed) {
			return skills.Evidence{}, ErrAdmission
		}
		proof, err := guarded.Validate(call, version)
		observed = append(observed, memorySecrets(s.settings, s.secret)...)
		if err != nil || !skillActivationClean(expected, identities, observed) {
			return skills.Evidence{}, ErrAdmission
		}
		return proof, nil
	})
	receipt, err = store.RevalidateAndRollbackOnce(ctx, operation, validatorID, expected, operationGuard)
	secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
	if err != nil || receipt.Validate() != nil || receipt.OperationID != operation || receipt.ValidatorID != validatorID || receipt.Expected != expected || ctx.Err() != nil || !skillRegressionOperationClean(receipt, secrets) {
		return bad()
	}
	return receipt, nil
}

func skillRegressionOperationClean(receipt skills.RegressionOperation, secrets []string) bool {
	return skillActivationClean(receipt, []string{receipt.OperationID, receipt.ValidatorID, receipt.Expected.Key.Scope, receipt.Expected.Key.Name, receipt.Expected.Active, receipt.Expected.Revision, receipt.After.Active, receipt.After.Revision, receipt.Result.Evidence.ID}, secrets)
}
