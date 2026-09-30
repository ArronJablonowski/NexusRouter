package app

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"github.com/ArronJablonowski/NexusRouter/skills"
)

// RevalidateSkillVersion runs one trusted, read-only deterministic regression
// check and may restore the previous validated version. It is not a scheduler
// or a remote proof-submission endpoint. Rollback policy is independent of new
// activation policy. Passing evidence leaves the activation history unchanged.
// State in a successful result is the checked observation, not a new revision;
// callers should inspect current state afterward, particularly after any error.
func (s *Service) RevalidateSkillVersion(ctx context.Context, expected skills.ActivationState, validator skills.Validator) (skills.RegressionResult, error) {
	bad := func() (skills.RegressionResult, error) { return skills.RegressionResult{}, ErrAdmission }
	if ctx == nil || !s.skillActivationConfigured(expected.Key) || !s.settings.Skills.Rollback || expected.Validate() != nil || !skillActivationID(expected.Active) || validator == nil || nilSkillActivationValidator(validator) {
		return bad()
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if ctx.Err() != nil {
		return bad()
	}
	secrets := memorySecrets(s.settings, s.secret)
	if !skillActivationClean(expected, []string{expected.Key.Scope, expected.Key.Name, expected.Active, expected.Revision}, secrets) {
		return bad()
	}
	read, err := skills.OpenReadOnly(s.settings.Skills.Root, []string{expected.Key.Scope})
	if err != nil {
		return bad()
	}
	defer read.Close()
	current, err := read.ActivationState(ctx, expected.Key)
	if err != nil || current != expected {
		return bad()
	}
	candidate, err := read.Load(ctx, expected.Key, expected.Active)
	if err != nil || candidate.Validate() != nil || !skillActivationCandidateClean(candidate, secrets) {
		return bad()
	}
	candidateBody, err := json.Marshal(candidate)
	if err != nil || ctx.Err() != nil {
		return bad()
	}
	store, err := skills.Open(s.settings.Skills.Root, []string{expected.Key.Scope})
	if err != nil {
		return bad()
	}
	defer store.Close()
	store.SetAutomatic(true)
	guarded := s.guardSkillValidator(expected, candidate, candidateBody, secrets, validator)
	result, err := store.RevalidateAndRollback(ctx, expected, guarded)
	if err != nil {
		return bad()
	}
	return result, nil
}

// The same callback boundary protects activation and regression. Trusted
// validators must cooperate with cancellation, be concurrency-safe and avoid
// side effects; this guard is not a sandbox or an atomic secret-rotation lock.
func (s *Service) guardSkillValidator(expected skills.ActivationState, candidate skills.Version, candidateBody []byte, secrets []string, validator skills.Validator) skills.Validator {
	return skills.ValidatorFunc(func(callCtx context.Context, actual skills.Version) (skills.Evidence, error) {
		// Rebind after reopening the catalog so a changed version cannot enter
		// the callback under a previously inspected identity.
		body, err := json.Marshal(actual)
		entrySecrets := append(append([]string(nil), secrets...), memorySecrets(s.settings, s.secret)...)
		identities := []string{expected.Key.Scope, expected.Key.Name, expected.Active, expected.Revision, candidate.ID}
		if err != nil || !bytes.Equal(body, candidateBody) || actual.Validate() != nil || !skillActivationCandidateClean(actual, entrySecrets) || !skillActivationClean(expected, identities, entrySecrets) {
			return skills.Evidence{}, skills.ErrValidation
		}
		proof, err := validator.Validate(callCtx, actual)
		// Keep every observed secret and inspect the owned pre-callback version,
		// not slices the validator may have changed.
		currentSecrets := append(entrySecrets, memorySecrets(s.settings, s.secret)...)
		if err != nil || redact(proof.ID, currentSecrets) != proof.ID || !skillActivationCandidateClean(candidate, currentSecrets) || !skillActivationClean(expected, identities, currentSecrets) {
			return skills.Evidence{}, skills.ErrValidation
		}
		return proof, nil
	})
}
