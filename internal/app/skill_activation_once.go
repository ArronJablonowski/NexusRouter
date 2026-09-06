package app

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/skills"
)

// SkillActivationOperation inspects a durable operation receipt without creating
// a catalog or inferring authority to change its current activation.
func (s *Service) SkillActivationOperation(ctx context.Context, key skills.Key, operation string) (skills.ActivationOperation, error) {
	zero := skills.ActivationOperation{}
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
	receipt, err := store.ActivationOperation(ctx, key, operation)
	secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
	if err != nil || receipt.Validate() != nil || receipt.OperationID != operation || receipt.Expected.Key != key || ctx.Err() != nil || !skillActivationClean(receipt, []string{operation, key.Scope, key.Name, receipt.Candidate, receipt.Expected.Active, receipt.Expected.Revision}, secrets) {
		return zero, ErrAdmission
	}
	return receipt, nil
}

// ActivateSkillVersionOnce binds one operation ID to an exact activation. A
// matching receipt acknowledges the original operation without validation or
// reactivation, even when later transitions have changed the active version.
func (s *Service) ActivateSkillVersionOnce(ctx context.Context, operation string, expected skills.ActivationState, id string, validator skills.Validator) error {
	if ctx == nil || !s.skillActivationConfigured(expected.Key) || !s.settings.Skills.AutoActivate || !skillGenerationIdentifier.MatchString(operation) || expected.Validate() != nil || !skillActivationID(id) || validator == nil || nilSkillActivationValidator(validator) {
		return ErrAdmission
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	secrets := memorySecrets(s.settings, s.secret)
	if ctx.Err() != nil || !skillActivationClean(expected, []string{operation, expected.Key.Scope, expected.Key.Name, expected.Active, expected.Revision, id}, secrets) {
		return ErrAdmission
	}
	read, err := skills.OpenReadOnly(s.settings.Skills.Root, []string{expected.Key.Scope})
	if err != nil {
		return ErrAdmission
	}
	defer read.Close()
	receipt, err := read.ActivationOperation(ctx, expected.Key, operation)
	if err == nil {
		observedSecrets := append(secrets, memorySecrets(s.settings, s.secret)...)
		if receipt.Validate() != nil || receipt.OperationID != operation || receipt.Expected != expected || receipt.Candidate != id || ctx.Err() != nil || !skillActivationClean(receipt, []string{operation, expected.Key.Scope, expected.Key.Name, expected.Active, expected.Revision, id}, observedSecrets) {
			return ErrAdmission
		}
		return nil
	}
	if !errors.Is(err, skills.ErrNotFound) {
		return ErrAdmission
	}
	current, err := read.ActivationState(ctx, expected.Key)
	if err != nil || current != expected {
		return ErrAdmission
	}
	candidate, err := read.Load(ctx, expected.Key, id)
	if err != nil || candidate.Validate() != nil || !skillActivationCandidateClean(candidate, secrets) {
		return ErrAdmission
	}
	body, err := json.Marshal(candidate)
	if err != nil || ctx.Err() != nil {
		return ErrAdmission
	}
	store, err := skills.Open(s.settings.Skills.Root, []string{expected.Key.Scope})
	if err != nil {
		return ErrAdmission
	}
	defer store.Close()
	store.SetAutomatic(true)
	guarded := s.guardSkillValidator(expected, candidate, body, secrets, validator)
	operationGuard := skills.ValidatorFunc(func(ctx context.Context, version skills.Version) (skills.Evidence, error) {
		clean := func() bool {
			observed := append(append([]string(nil), secrets...), memorySecrets(s.settings, s.secret)...)
			return skillActivationClean(operation, []string{operation}, observed)
		}
		if !clean() {
			return skills.Evidence{}, ErrAdmission
		}
		proof, err := guarded.Validate(ctx, version)
		if err != nil || !clean() {
			return skills.Evidence{}, ErrAdmission
		}
		return proof, nil
	})
	if store.ActivateOnce(ctx, operation, expected, id, operationGuard, true) != nil {
		return ErrAdmission
	}
	return nil
}
