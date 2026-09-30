package v1

import (
	"context"

	"github.com/ArronJablonowski/NexusRouter/skills"
)

// ConsumeSkillWorkflowScan durably groups one saved scan page in the configured
// scope. The same expected revision returns the same historical receipt, without
// repeating updates. No provider or skill publication is invoked.
func (c *Client) ConsumeSkillWorkflowScan(ctx context.Context, name string, expectedRevision int64) (skills.WorkflowScanConsumption, error) {
	if !c.valid(ctx) {
		return skills.WorkflowScanConsumption{}, ErrAdmission
	}
	return c.service.ConsumeSkillWorkflowScan(ctx, name, expectedRevision)
}

// SkillWorkflowScanConsumption inspects saved consumption progress without
// creating storage or requiring automatic drafting to remain enabled.
func (c *Client) SkillWorkflowScanConsumption(ctx context.Context, name string) (skills.WorkflowScanConsumption, error) {
	if !c.valid(ctx) {
		return skills.WorkflowScanConsumption{}, ErrAdmission
	}
	return c.service.SkillWorkflowScanConsumption(ctx, name)
}

// SkillWorkflowScanBuckets lists grouping observations, including singletons,
// ordered by ID within one epoch. Advance afterID to the last returned ID. These
// observations must be revalidated through selection planning before generation.
func (c *Client) SkillWorkflowScanBuckets(ctx context.Context, name string, epoch int64, afterID string, limit int) ([]skills.WorkflowBucket, error) {
	if !c.valid(ctx) {
		return nil, ErrAdmission
	}
	return c.service.SkillWorkflowScanBuckets(ctx, name, epoch, afterID, limit)
}
