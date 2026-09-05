package v1

import (
	"context"

	"github.com/ArronJablonowski/DarwinRouter/skills"
)

// AdvanceSkillWorkflowScan durably records one bounded discovery page in the
// configured scope. Repeating the same expected revision returns its historical
// page without rescanning. This never dispatches generation or opens a skill root.
func (c *Client) AdvanceSkillWorkflowScan(ctx context.Context, name, domain string, expectedRevision int64, scanLimit int) (skills.WorkflowScanPage, error) {
	if !c.valid(ctx) {
		return skills.WorkflowScanPage{}, ErrAdmission
	}
	return c.service.AdvanceSkillWorkflowScan(ctx, name, domain, expectedRevision, scanLimit)
}

// SkillWorkflowScan inspects the current configured-scope scan head without
// advancing it, initializing storage or performing inference.
func (c *Client) SkillWorkflowScan(ctx context.Context, name string) (skills.WorkflowScan, error) {
	if !c.valid(ctx) {
		return skills.WorkflowScan{}, ErrAdmission
	}
	return c.service.SkillWorkflowScan(ctx, name)
}
