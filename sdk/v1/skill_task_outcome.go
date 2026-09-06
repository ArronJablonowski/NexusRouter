package v1

import (
	"context"

	"github.com/ArronJablonowski/DarwinRouter/skills"
)

type SkillTaskOutcome = skills.TaskOutcome

// SkillTaskOutcome returns one coherent metadata observation. Skill references
// describe freshly included context, not successful execution or causality.
// Inspection stays available when learning is disabled and never enables it.
func (c *Client) SkillTaskOutcome(ctx context.Context, task string) (SkillTaskOutcome, error) {
	if !c.valid(ctx) {
		return SkillTaskOutcome{}, ErrAdmission
	}
	return c.service.SkillTaskOutcome(ctx, task)
}
