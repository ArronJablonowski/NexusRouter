package v1

import (
	"context"

	"github.com/ArronJablonowski/DarwinRouter/skills"
)

// DiscoverSkillWorkflows reads candidate metadata without inference, publication
// or storage initialization. scanLimit is 1–20. Candidate selection does not
// replace the source and policy checks performed when generating a draft.
func (c *Client) DiscoverSkillWorkflows(ctx context.Context, domain, after string, scanLimit int) (skills.WorkflowCandidatePage, error) {
	if !c.valid(ctx) {
		return skills.WorkflowCandidatePage{}, ErrAdmission
	}
	return c.service.DiscoverSkillWorkflows(ctx, domain, after, scanLimit)
}
