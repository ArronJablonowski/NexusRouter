package v1

import (
	"context"

	"github.com/ArronJablonowski/NexusRouter/skills"
)

// PlanWorkflowSelection persists a content-addressed observation of explicitly
// chosen tasks and effective generation policy without performing inference.
// Group and algorithm identify the host's grouping rule, not semantic proof or
// source authorization. Identical admitted inputs retain their original record.
func (c *Client) PlanWorkflowSelection(ctx context.Context, modelID string, key skills.Key, group, algorithm string, taskIDs []string, maxCost float64) (skills.WorkflowSelection, error) {
	if !c.valid(ctx) {
		return skills.WorkflowSelection{}, ErrAdmission
	}
	return c.service.PlanWorkflowSelection(ctx, modelID, key, group, algorithm, taskIDs, maxCost)
}

// GenerateSkillSelection rechecks a saved selection's source and policy
// bindings, then claims its stable ID for one generation attempt. Reuse never
// authorizes redispatch, including after an uncertain result or restart. A
// successful result is an inactive proposal, not publication or activation.
func (c *Client) GenerateSkillSelection(ctx context.Context, selectionID string, maxCost float64) (skills.GenerationAttempt, error) {
	if !c.valid(ctx) {
		return skills.GenerationAttempt{}, ErrAdmission
	}
	return c.service.GenerateSkillSelection(ctx, selectionID, maxCost)
}
