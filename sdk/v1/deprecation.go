package v1

import (
	"context"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
)

type DeprecationRequest = evaluation.DeprecationRequest
type DeprecationPolicy = evaluation.DeprecationPolicy
type DeprecationReport = evaluation.DeprecationReport

// ModelDeprecation inspects bounded persisted evidence without inference or
// storage initialization. A recommendation never authorizes model removal.
func (c *Client) ModelDeprecation(ctx context.Context, request DeprecationRequest) (DeprecationReport, error) {
	if request.Validate() != nil || !c.valid(ctx) || ctx.Err() != nil {
		return DeprecationReport{}, ErrAdmission
	}
	report, err := c.service.ModelDeprecation(ctx, request.ModelID, request.Domain, request.Profile, request.Policy)
	if err != nil || ctx.Err() != nil || report.Validate() != nil || report.ConfiguredModelID == "" || report.Policy != request.Policy {
		return DeprecationReport{}, ErrAdmission
	}
	return report, nil
}
