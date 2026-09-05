package app

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/skills"
)

// SkillActivationState inspects the configured file catalog without creating or
// repairing it. Injected retrieval stores are not implicitly treated as mutable
// activation stores. The returned revision is a precondition, not approval.
func (s *Service) SkillActivationState(ctx context.Context, key skills.Key) (skills.ActivationState, error) {
	if ctx == nil || !s.skillActivationConfigured(key) {
		return skills.ActivationState{}, ErrAdmission
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if ctx.Err() != nil {
		return skills.ActivationState{}, ErrAdmission
	}
	secrets := memorySecrets(s.settings, s.secret)
	if !skillActivationClean(key, []string{key.Scope, key.Name}, secrets) {
		return skills.ActivationState{}, ErrAdmission
	}
	store, err := skills.OpenReadOnly(s.settings.Skills.Root, []string{key.Scope})
	if err != nil {
		return skills.ActivationState{}, ErrAdmission
	}
	defer store.Close()
	state, err := store.ActivationState(ctx, key)
	observedSecrets := append(append([]string(nil), secrets...), memorySecrets(s.settings, s.secret)...)
	if err != nil || state.Validate() != nil || !skillActivationClean(state, []string{state.Key.Scope, state.Key.Name, state.Active, state.Revision}, observedSecrets) || ctx.Err() != nil {
		return skills.ActivationState{}, ErrAdmission
	}
	return state, nil
}

// ActivateSkillVersion requires a trusted deterministic validator, never an
// untrusted proof or model judgment. Validators must be read-only, retry-safe,
// concurrency-safe and cooperative with cancellation; they are not sandboxed.
// This in-process surface is not exposed as a CLI/HTTP proof-submission endpoint.
// Inspect activation state afterward to distinguish successful transitions.
func (s *Service) ActivateSkillVersion(ctx context.Context, expected skills.ActivationState, id string, validator skills.Validator) error {
	if ctx == nil || !s.skillActivationConfigured(expected.Key) || !s.settings.Skills.AutoActivate || expected.Validate() != nil || !skillActivationID(id) || validator == nil || nilSkillActivationValidator(validator) {
		return ErrAdmission
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if ctx.Err() != nil {
		return ErrAdmission
	}
	secrets := memorySecrets(s.settings, s.secret)
	if !skillActivationClean(expected, []string{expected.Key.Scope, expected.Key.Name, expected.Active, expected.Revision, id}, secrets) {
		return ErrAdmission
	}
	read, err := skills.OpenReadOnly(s.settings.Skills.Root, []string{expected.Key.Scope})
	if err != nil {
		return ErrAdmission
	}
	defer read.Close()
	current, err := read.ActivationState(ctx, expected.Key)
	if err != nil || current != expected {
		return ErrAdmission
	}
	candidate, err := read.Load(ctx, expected.Key, id)
	if err != nil || candidate.Validate() != nil || !skillActivationCandidateClean(candidate, secrets) {
		return ErrAdmission
	}
	candidateBody, err := json.Marshal(candidate)
	if err != nil || ctx.Err() != nil {
		return ErrAdmission
	}
	store, err := skills.Open(s.settings.Skills.Root, []string{expected.Key.Scope})
	if err != nil {
		return ErrAdmission
	}
	defer store.Close()
	store.SetAutomatic(true)
	guarded := s.guardSkillValidator(expected, candidate, candidateBody, secrets, validator)
	if store.ActivateAt(ctx, expected, id, guarded, true) != nil {
		return ErrAdmission
	}
	return nil
}

func (s *Service) skillActivationConfigured(key skills.Key) bool {
	return s != nil && s.settings.Validate() == nil && s.settings.Skills.Enabled && s.settings.Skills.Root != "" && key.Scope == s.settings.Skills.Scope && skillGenerationIdentifier.MatchString(key.Scope) && skillGenerationIdentifier.MatchString(key.Name) && s.skillStore == nil
}

func skillActivationID(id string) bool {
	if len(id) != 32 || id != strings.ToLower(id) {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func nilSkillActivationValidator(validator skills.Validator) bool {
	value := reflect.ValueOf(validator)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	}
	return false
}

func skillActivationCandidateClean(version skills.Version, secrets []string) bool {
	draft := version.Draft
	values := []string{version.ID, version.Parent, draft.Key.Scope, draft.Key.Name, draft.Description, draft.Configuration}
	for _, list := range [][]string{draft.Tags, draft.SourceSessions, draft.SourceEvidence, draft.Steps, draft.RequiredTools, draft.Risks, draft.ValidationCases} {
		values = append(values, list...)
	}
	return skillActivationClean(version, values, secrets)
}

func skillActivationClean(record any, values, secrets []string) bool {
	for _, value := range values {
		if redact(value, secrets) != value {
			return false
		}
	}
	body, err := json.Marshal(record)
	return err == nil && redact(string(body), secrets) == string(body)
}
