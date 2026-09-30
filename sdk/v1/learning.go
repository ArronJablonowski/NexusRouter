package v1

import (
	"context"

	"github.com/ArronJablonowski/NexusRouter/skills"
)

// SkillLearningState inspects the configured learner's durable phase/cursor.
// It does not start background work, create storage, dispatch a model, publish a
// draft or change activation. Inspection remains available with learning off.
func (c *Client) SkillLearningState(ctx context.Context) (skills.LearningState, error) {
	if !c.valid(ctx) {
		return skills.LearningState{}, ErrAdmission
	}
	return c.service.SkillLearningState(ctx)
}
