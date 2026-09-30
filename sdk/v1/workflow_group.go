package v1

import (
	"context"

	"github.com/ArronJablonowski/NexusRouter/skills"
)

// GroupSkillWorkflows reads accepted task observations and groups identical
// ordered tool names, domain and profile. It performs no inference or writes.
// Groups are drafting heuristics, not proof of equivalent work or source tenancy.
func (c *Client) GroupSkillWorkflows(ctx context.Context, taskIDs []string) ([]skills.WorkflowGroup, error) {
	if !c.valid(ctx) {
		return nil, ErrAdmission
	}
	return c.service.GroupSkillWorkflows(ctx, taskIDs)
}

// PlanGroupedWorkflowSelection derives and saves a verified tool-sequence group.
// All input tasks must qualify in exactly one group with distinct sessions.
// No model is invoked and no skill is published or activated.
func (c *Client) PlanGroupedWorkflowSelection(ctx context.Context, modelID string, key skills.Key, taskIDs []string, maxCost float64) (skills.WorkflowSelection, error) {
	if !c.valid(ctx) {
		return skills.WorkflowSelection{}, ErrAdmission
	}
	return c.service.PlanGroupedWorkflowSelection(ctx, modelID, key, taskIDs, maxCost)
}
