package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/skills"
)

// DurableSkillRegressionStep prepares one persisted check before validation and
// reconciles its result. Pending identities survive restart; a due-time pause
// never invokes a validator. A failed check may return advanced state and an
// error: inspect its durable outcome rather than treating cursor movement as
// successful validation. This explicitly invoked Go-host path starts no daemon.
func (s *Service) DurableSkillRegressionStep(ctx context.Context, name, validatorID string, interval time.Duration, validator skills.Validator) (skills.RegressionMonitorState, error) {
	zero := skills.RegressionMonitorState{}
	if ctx == nil || ctx.Err() != nil || !s.skillRegressionConfigured(validator) {
		return zero, ErrAdmission
	}
	policy, err := s.regressionMonitorPolicy(name, validatorID, interval)
	if err != nil {
		return zero, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	secrets := memorySecrets(s.settings, s.secret)
	identities := []string{s.settings.Skills.Scope, name, validatorID, policy}
	if !selectionValueClean(identities, secrets) {
		return zero, ErrAdmission
	}
	// Require an existing compatible catalog before opening mutation control.
	read, err := skills.OpenReadOnly(s.settings.Skills.Root, []string{s.settings.Skills.Scope})
	if err != nil {
		return zero, ErrAdmission
	}
	defer read.Close()
	store, err := skills.Open(s.settings.Skills.Root, []string{s.settings.Skills.Scope})
	if err != nil {
		return zero, ErrAdmission
	}
	defer store.Close()
	store.SetAutomatic(true)
	guard := skills.RegressionMonitorGuard(func(call context.Context, state skills.RegressionMonitorState, check skills.RegressionMonitorCheck) error {
		secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
		current, err := s.regressionMonitorPolicy(name, validatorID, interval)
		if call.Err() != nil || err != nil || current != policy || state.Scope != s.settings.Skills.Scope || state.Name != name || state.ValidatorID != validatorID || state.PolicyDigest != policy || state.Interval != interval || !selectionValueClean(identities, secrets) || !selectionValueClean(state, secrets) || !selectionValueClean(check, secrets) {
			return ErrAdmission
		}
		return nil
	})
	state, check, err := store.PrepareRegressionMonitor(ctx, s.settings.Skills.Scope, name, validatorID, policy, interval, guard)
	if err != nil || state.Validate() != nil || guard(ctx, state, check) != nil {
		return zero, ErrAdmission
	}
	if check.OperationID == "" {
		return state, nil
	}
	if check.Validate() != nil || check.Status != "pending" || check.Expected.Key.Scope != s.settings.Skills.Scope || check.Scope != state.Scope || check.MonitorName != name || check.ValidatorID != validatorID || check.PolicyDigest != policy || check.OperationID != state.PendingOperationID || check.Sequence != state.Revision {
		return zero, ErrAdmission
	}
	// The core can finish a stale intent without validation. Load the immutable
	// candidate lazily inside the guarded callback so stale catalog state cannot
	// prevent that safe, audited cursor progression.
	wrapped := skills.ValidatorFunc(func(call context.Context, actual skills.Version) (skills.Evidence, error) {
		candidate, err := read.Load(call, check.Expected.Key, check.Expected.Active)
		secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
		if err != nil || candidate.Validate() != nil || !skillActivationCandidateClean(candidate, secrets) || !selectionValueClean(check, secrets) {
			return skills.Evidence{}, ErrAdmission
		}
		body, err := json.Marshal(candidate)
		if err != nil {
			return skills.Evidence{}, ErrAdmission
		}
		proof, err := s.guardSkillValidator(check.Expected, candidate, body, secrets, validator).Validate(call, actual)
		// Keep the exact returned identity visible to later completion guards.
		identities = append(identities, proof.ID)
		return proof, err
	})
	state, executionErr := store.ExecuteRegressionMonitorCheck(ctx, check, wrapped, guard)
	final, inspectErr := store.RegressionMonitorCheck(ctx, check.Scope, check.MonitorName, check.OperationID)
	secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
	if inspectErr != nil || final.Validate() != nil || state.Validate() != nil || guard(ctx, state, final) != nil {
		return zero, ErrAdmission
	}
	if final.Status == "completed" {
		receipt, err := read.RegressionOperation(ctx, check.Expected.Key, check.OperationID)
		secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
		if err != nil || receipt.Validate() != nil || receipt.Expected != check.Expected || receipt.ValidatorID != validatorID || !skillRegressionOperationClean(receipt, secrets) {
			return zero, ErrAdmission
		}
	}
	if executionErr != nil || ctx.Err() != nil || final.Status != "completed" {
		return state, ErrAdmission
	}
	return state, nil
}

func (s *Service) regressionMonitorPolicy(name, validatorID string, interval time.Duration) (string, error) {
	if s == nil || !s.skillActivationConfigured(skills.Key{Scope: s.settings.Skills.Scope, Name: name}) || !s.settings.Skills.Rollback || !skillGenerationIdentifier.MatchString(validatorID) || interval < time.Second || interval > 24*time.Hour {
		return "", ErrAdmission
	}
	body, err := json.Marshal(struct {
		Version                      int
		Mode, Scope, Name, Validator string
		LocalOnly                    bool
		Interval                     time.Duration
	}{1, s.settings.Mode, s.settings.Skills.Scope, name, validatorID, s.settings.Skills.LocalOnly, interval})
	if err != nil {
		return "", ErrAdmission
	}
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:]), nil
}
