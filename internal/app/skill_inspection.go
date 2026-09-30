package app

import (
	"context"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/skills"
)

// InspectSkillGeneration reads a scope-bound saved proposal. It never creates
// or migrates storage, dispatches inference, or publishes/activates a workflow.
// Full records can contain sensitive generated content; protect exports.
func InspectSkillGeneration(ctx context.Context, path, scope, id string) (skills.GenerationAttempt, error) {
	if ctx == nil || !skillGenerationIdentifier.MatchString(scope) || !skillGenerationIdentifier.MatchString(id) {
		return skills.GenerationAttempt{}, ErrInspection
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	db, err := telemetry.OpenReadOnly(ctx, path)
	if err != nil {
		return skills.GenerationAttempt{}, ErrInspection
	}
	defer db.Close()
	a, err := db.SkillGenerationAttempt(ctx, id)
	if err != nil || a.Key.Scope != scope || ctx.Err() != nil {
		return skills.GenerationAttempt{}, ErrInspection
	}
	return a, nil
}

// ListSkillGenerations returns metadata only, in lexical ID order. Pages are
// live observations; a cursor is not a frozen snapshot or execution authority.
func ListSkillGenerations(ctx context.Context, path, scope, after string, limit int) ([]skills.GenerationSummary, error) {
	if ctx == nil || !skillGenerationIdentifier.MatchString(scope) || (after != "" && !skillGenerationIdentifier.MatchString(after)) || limit < 1 || limit > 100 {
		return nil, ErrInspection
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	db, err := telemetry.OpenReadOnly(ctx, path)
	if err != nil {
		return nil, ErrInspection
	}
	defer db.Close()
	records, err := db.ListSkillGenerationAttempts(ctx, scope, after, limit)
	if err != nil || ctx.Err() != nil {
		return nil, ErrInspection
	}
	result := make([]skills.GenerationSummary, 0, len(records))
	for _, a := range records {
		result = append(result, a.Summary())
	}
	return result, nil
}
