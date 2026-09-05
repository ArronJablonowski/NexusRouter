package v1

import (
	"context"

	"github.com/ArronJablonowski/DarwinRouter/skills"
)

// RevalidateSkillVersion invokes a trusted deterministic validator under the
// configured rollback policy. Callbacks must be read-only, retry-safe and
// cancellation-cooperative, not model judgments. Passing checks do not mutate
// activation state; failure may roll back even when new activation is disabled.
// The result's State is the checked observation. Inspect current state afterward.
func (c *Client) RevalidateSkillVersion(ctx context.Context, expected skills.ActivationState, validator skills.Validator) (skills.RegressionResult, error) {
	if !c.valid(ctx) {
		return skills.RegressionResult{}, ErrAdmission
	}
	return c.service.RevalidateSkillVersion(ctx, expected, validator)
}
