package v1

import (
	"context"

	"github.com/ArronJablonowski/NexusRouter/skills"
)

// GenerateSkillDraft proposes a procedural workflow from completed tasks with
// accepted durable feedback. It records a generation attempt, but neither
// publishes a version nor activates or mutates the configured SkillStore.
// Explicit automatic-drafting configuration, scope, model and cost admission
// still apply. A reused attempt ID never silently dispatches another generation.
func (c *Client) GenerateSkillDraft(ctx context.Context, attemptID, modelID string, key skills.Key, taskIDs []string, maxCost float64) (skills.GenerationAttempt, error) {
	if !c.valid(ctx) {
		return skills.GenerationAttempt{}, ErrAdmission
	}
	return c.service.GenerateSkillDraft(ctx, attemptID, modelID, key, taskIDs, maxCost)
}
