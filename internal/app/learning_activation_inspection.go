package app

import (
	"context"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/skills"
)

// SkillLearningActivationIntent reads a pinned activation intent without
// starting learning, opening the skill catalog, or initializing storage.
// Inspection remains available when learning is disabled.
func (s *Service) SkillLearningActivationIntent(ctx context.Context, selection string) (skills.LearningActivationIntent, error) {
	zero := skills.LearningActivationIntent{}
	if s == nil || ctx == nil || ctx.Err() != nil || !skillGenerationIdentifier.MatchString(s.settings.Skills.Scope) || !skillGenerationIdentifier.MatchString(s.settings.Skills.Learning.Name) || !skillGenerationIdentifier.MatchString(selection) {
		return zero, ErrLearningAttention
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	scope, name := s.settings.Skills.Scope, s.settings.Skills.Learning.Name
	secrets := memorySecrets(s.settings, s.secret)
	if !selectionValueClean([]string{scope, name, selection}, secrets) {
		return zero, ErrLearningAttention
	}
	db, err := telemetry.OpenReadOnly(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return zero, ErrLearningAttention
	}
	defer db.Close()
	intent, err := db.LearningActivationIntent(ctx, scope, name, selection)
	secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
	if err != nil || intent.Validate() != nil || intent.Scope != scope || intent.Name != name || intent.SelectionID != selection || ctx.Err() != nil || !selectionValueClean(intent, secrets) {
		return zero, ErrLearningAttention
	}
	return intent, nil
}
