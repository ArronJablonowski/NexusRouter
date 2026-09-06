package app

import (
	"context"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/routing"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

// SelectSkillComparison chooses insertion-order windows, never success-only
// examples. Selection, correlation checks and current outcomes share a snapshot.
func (s *Service) SelectSkillComparison(ctx context.Context, request skills.ComparisonSelectionRequest) (out skills.ComparisonSelectionReport, err error) {
	defer func() {
		if recover() != nil {
			out, err = skills.ComparisonSelectionReport{}, ErrInspection
		}
	}()
	if ctx == nil || ctx.Err() != nil {
		return out, ErrAdmission
	}
	policy, err := s.skillComparisonSelectionPolicy(request)
	if err != nil {
		return out, ErrAdmission
	}
	secrets := memorySecrets(s.settings, s.secret)
	if !selectionValueClean([]any{request, policy}, secrets) {
		return out, ErrInspection
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	db, err := telemetry.OpenReadOnly(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return out, ErrInspection
	}
	defer db.Close()
	out, observations, err := db.SkillComparisonSelection(ctx, policy)
	if err != nil || ctx.Err() != nil || out.Policy != policy || out.Validate() != nil {
		return skills.ComparisonSelectionReport{}, ErrInspection
	}
	for _, observation := range observations {
		if observation.Validate() != nil || observation.SkillContext == nil {
			return skills.ComparisonSelectionReport{}, ErrInspection
		}
		for _, ref := range observation.SkillContext.References {
			if ref.Scope != policy.Comparison.Key.Scope {
				return skills.ComparisonSelectionReport{}, ErrInspection
			}
		}
	}
	secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
	if !selectionValueClean([]any{request, policy, observations, out}, secrets) || ctx.Err() != nil {
		return skills.ComparisonSelectionReport{}, ErrInspection
	}
	out.ConfiguredModelID = request.ModelID
	if out.Comparison != nil {
		out.Comparison.ConfiguredModelID = request.ModelID
	}
	secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
	if out.Validate() != nil || !selectionValueClean([]any{request, policy, observations, out}, secrets) || ctx.Err() != nil {
		return skills.ComparisonSelectionReport{}, ErrInspection
	}
	return out, nil
}

func (s *Service) skillComparisonSelectionPolicy(request skills.ComparisonSelectionRequest) (skills.ComparisonSelectionPolicy, error) {
	if s == nil || request.Validate() != nil || s.settings.Validate() != nil || !skillGenerationIdentifier.MatchString(s.settings.Skills.Scope) {
		return skills.ComparisonSelectionPolicy{}, ErrAdmission
	}
	policy := skills.ComparisonSelectionPolicy{Version: 1, Privacy: request.Privacy, TasksPerVersion: request.TasksPerVersion, Comparison: skills.ComparisonPolicy{Key: skills.Key{Scope: s.settings.Skills.Scope, Name: request.Name}, BaselineVersion: request.BaselineVersion, CandidateVersion: request.CandidateVersion, Source: request.Source, MinSamples: request.MinSamples, MinDrop: request.MinDrop}}
	for _, model := range s.settings.Models {
		if model.ID == request.ModelID {
			policy.Comparison.Execution = routing.Key{Model: model.Model, Provider: model.Provider, Domain: request.Domain, Profile: request.Profile}
			break
		}
	}
	if policy.Validate() != nil {
		return skills.ComparisonSelectionPolicy{}, ErrAdmission
	}
	return policy, nil
}
