package app

import (
	"context"
	"slices"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/routing"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

// CompareSkillOutcomes is a read-only advisory comparison of explicit tasks.
// It grants no activation/rollback authority and never invokes a model or skill
// catalog. Disabled retrieval/learning does not disable historical inspection.
func (s *Service) CompareSkillOutcomes(ctx context.Context, request skills.ComparisonRequest) (out skills.ComparisonReport, err error) {
	defer func() {
		if recover() != nil {
			out, err = skills.ComparisonReport{}, ErrInspection
		}
	}()
	if s == nil || ctx == nil || ctx.Err() != nil || request.Validate() != nil || s.settings.Validate() != nil || !skillGenerationIdentifier.MatchString(s.settings.Skills.Scope) {
		return out, ErrAdmission
	}
	request.Tasks = append([]string(nil), request.Tasks...)
	policy := skills.ComparisonPolicy{Key: skills.Key{Scope: s.settings.Skills.Scope, Name: request.Name}, BaselineVersion: request.BaselineVersion, CandidateVersion: request.CandidateVersion, Source: request.Source, MinSamples: request.MinSamples, MinDrop: request.MinDrop}
	for _, model := range s.settings.Models {
		if model.ID == request.ModelID {
			policy.Execution = routing.Key{Model: model.Model, Provider: model.Provider, Domain: request.Domain, Profile: request.Profile}
			break
		}
	}
	if policy.Validate() != nil {
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
	observations, err := db.SkillTaskOutcomes(ctx, request.Tasks)
	if err != nil || len(observations) != len(request.Tasks) || ctx.Err() != nil {
		return skills.ComparisonReport{}, ErrInspection
	}
	ids := append([]string(nil), request.Tasks...)
	slices.Sort(ids)
	for i, item := range observations {
		if item.Validate() != nil || item.TaskID != ids[i] {
			return skills.ComparisonReport{}, ErrInspection
		}
		if item.SkillContext != nil {
			for _, ref := range item.SkillContext.References {
				if ref.Scope != policy.Key.Scope {
					return skills.ComparisonReport{}, ErrInspection
				}
			}
		}
	}
	// Check the full evidence, not just the aggregate report which hides IDs.
	secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
	if !selectionValueClean([]any{request, policy, observations}, secrets) || ctx.Err() != nil {
		return skills.ComparisonReport{}, ErrInspection
	}
	out, err = skills.CompareTaskOutcomes(observations, policy)
	out.ConfiguredModelID = request.ModelID
	if err != nil || out.Validate() != nil || out.Policy != policy {
		return skills.ComparisonReport{}, ErrInspection
	}
	secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
	if !selectionValueClean([]any{request, policy, observations, out}, secrets) || ctx.Err() != nil {
		return skills.ComparisonReport{}, ErrInspection
	}
	return out, nil
}
