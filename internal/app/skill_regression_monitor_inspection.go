package app

import (
	"context"
	"time"

	"github.com/ArronJablonowski/NexusRouter/skills"
)

// SkillRegressionMonitorState reads saved scheduling state, not current health
// or permission to dispatch. Inspection remains available with rollback off.
func (s *Service) SkillRegressionMonitorState(ctx context.Context, name string) (skills.RegressionMonitorState, error) {
	zero := skills.RegressionMonitorState{}
	if ctx == nil || s == nil || !s.skillActivationConfigured(skills.Key{Scope: s.settings.Skills.Scope, Name: name}) {
		return zero, ErrAdmission
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	secrets := memorySecrets(s.settings, s.secret)
	if ctx.Err() != nil || !selectionValueClean([]string{name, s.settings.Skills.Scope}, secrets) {
		return zero, ErrAdmission
	}
	store, err := skills.OpenReadOnly(s.settings.Skills.Root, []string{s.settings.Skills.Scope})
	if err != nil {
		return zero, ErrAdmission
	}
	defer store.Close()
	state, err := store.RegressionMonitorState(ctx, s.settings.Skills.Scope, name)
	secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
	if err != nil || ctx.Err() != nil || state.Validate() != nil || state.Scope != s.settings.Skills.Scope || state.Name != name || !selectionValueClean(state, secrets) {
		return zero, ErrAdmission
	}
	return state, nil
}

// SkillRegressionMonitorCheck reads a retained intent/outcome. Failed outcomes
// are scheduler observations, not evidence that a skill passed validation.
func (s *Service) SkillRegressionMonitorCheck(ctx context.Context, name, operation string) (skills.RegressionMonitorCheck, error) {
	zero := skills.RegressionMonitorCheck{}
	if ctx == nil || s == nil || !s.skillActivationConfigured(skills.Key{Scope: s.settings.Skills.Scope, Name: name}) || !skillGenerationIdentifier.MatchString(operation) {
		return zero, ErrAdmission
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	secrets := memorySecrets(s.settings, s.secret)
	if ctx.Err() != nil || !selectionValueClean([]string{name, operation, s.settings.Skills.Scope}, secrets) {
		return zero, ErrAdmission
	}
	store, err := skills.OpenReadOnly(s.settings.Skills.Root, []string{s.settings.Skills.Scope})
	if err != nil {
		return zero, ErrAdmission
	}
	defer store.Close()
	check, err := store.RegressionMonitorCheck(ctx, s.settings.Skills.Scope, name, operation)
	secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
	if err != nil || ctx.Err() != nil || check.Validate() != nil || check.Scope != s.settings.Skills.Scope || check.MonitorName != name || check.OperationID != operation || !selectionValueClean(check, secrets) {
		return zero, ErrAdmission
	}
	return check, nil
}
