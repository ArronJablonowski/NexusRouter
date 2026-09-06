package v1

import (
	"context"

	"github.com/ArronJablonowski/DarwinRouter/skills"
)

type SkillComparisonSelectionRequest = skills.ComparisonSelectionRequest
type SkillComparisonSelectionReport = skills.ComparisonSelectionReport

// SelectSkillComparison selects bounded historical observations from the
// configured store. Its advisory result does not authorize skill mutation.
func (c *Client) SelectSkillComparison(ctx context.Context, request SkillComparisonSelectionRequest) (SkillComparisonSelectionReport, error) {
	if !c.valid(ctx) {
		return SkillComparisonSelectionReport{}, ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return SkillComparisonSelectionReport{}, err
	}
	return c.service.SelectSkillComparison(ctx, request)
}
