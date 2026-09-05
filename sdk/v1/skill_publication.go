package v1

import (
	"context"

	"github.com/ArronJablonowski/DarwinRouter/skills"
)

// PublishSkillGeneration publishes a saved proposal as an inactive file-store
// version. Repeating the same publication returns the same version; it does not
// activate a skill or establish that the proposal's sources are still current.
func (c *Client) PublishSkillGeneration(ctx context.Context, attemptID string) (skills.Version, error) {
	if !c.valid(ctx) {
		return skills.Version{}, ErrAdmission
	}
	return c.service.PublishSkillGeneration(ctx, attemptID)
}
