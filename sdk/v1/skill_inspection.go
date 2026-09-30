package v1

import (
	"context"

	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/skills"
)

// InspectSkillGeneration reads a scope-bound saved proposal or attempt. It never
// invokes a provider, initializes storage, or publishes or activates a skill.
func (c *Client) InspectSkillGeneration(ctx context.Context, scope, id string) (skills.GenerationAttempt, error) {
	if !c.valid(ctx) {
		return skills.GenerationAttempt{}, ErrAdmission
	}
	return app.InspectSkillGeneration(ctx, c.database, scope, id)
}

// ListSkillGenerations reads bounded metadata ordered by attempt ID, excluding
// proposal contents and source identities. Pagination is not a frozen snapshot.
func (c *Client) ListSkillGenerations(ctx context.Context, scope, after string, limit int) ([]skills.GenerationSummary, error) {
	if !c.valid(ctx) {
		return nil, ErrAdmission
	}
	return app.ListSkillGenerations(ctx, c.database, scope, after, limit)
}
