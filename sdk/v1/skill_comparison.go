package v1

import (
	"context"

	"github.com/ArronJablonowski/NexusRouter/skills"
)

type SkillComparisonRequest = skills.ComparisonRequest
type SkillComparisonReport = skills.ComparisonReport
type SkillComparisonPolicy = skills.ComparisonPolicy
type SkillComparisonCohort = skills.ComparisonCohort

// CompareSkillOutcomes observes an explicit bounded task set in the configured
// scope. The advisory report grants no activation or rollback authority and
// does not create storage, run validators, or dispatch models.
func (c *Client) CompareSkillOutcomes(ctx context.Context, request SkillComparisonRequest) (SkillComparisonReport, error) {
	if !c.valid(ctx) {
		return SkillComparisonReport{}, ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return SkillComparisonReport{}, err
	}
	return c.service.CompareSkillOutcomes(ctx, request)
}
