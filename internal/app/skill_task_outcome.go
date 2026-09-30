package app

import (
	"context"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/sessions"
	"github.com/ArronJablonowski/NexusRouter/skills"
)

// SkillTaskOutcome observes fresh skill-context attribution and independent
// task evidence without reopening the catalog, dispatching work or changing
// fitness. Missing quality evidence is unknown, never implicit acceptance.
func (s *Service) SkillTaskOutcome(ctx context.Context, task string) (out skills.TaskOutcome, err error) {
	defer func() {
		if recover() != nil {
			out, err = skills.TaskOutcome{}, ErrInspection
		}
	}()
	if s == nil || ctx == nil || ctx.Err() != nil || !sessions.ValidEventPageID(task) || (s.settings.Skills.Scope != "" && !skillGenerationIdentifier.MatchString(s.settings.Skills.Scope)) {
		return skills.TaskOutcome{}, ErrAdmission
	}
	secrets := memorySecrets(s.settings, s.secret)
	if !selectionValueClean([]string{task, s.settings.Skills.Scope}, secrets) {
		return skills.TaskOutcome{}, ErrInspection
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	db, err := telemetry.OpenReadOnly(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return skills.TaskOutcome{}, ErrInspection
	}
	defer db.Close()
	out, err = db.SkillTaskOutcome(ctx, task)
	if err != nil || out.TaskID != task || out.Validate() != nil || ctx.Err() != nil {
		return skills.TaskOutcome{}, ErrInspection
	}
	if out.SkillContext != nil {
		for _, ref := range out.SkillContext.References {
			if ref.Scope != s.settings.Skills.Scope {
				return skills.TaskOutcome{}, ErrInspection
			}
		}
	}
	// Identity metadata cannot be redacted into a different skill/evidence key.
	// Include fresh credentials in case the configured secret store rotated.
	secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
	if !selectionValueClean(out, secrets) || ctx.Err() != nil {
		return skills.TaskOutcome{}, ErrInspection
	}
	return out, nil
}
