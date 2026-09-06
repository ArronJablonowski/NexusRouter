package v1

import (
	"context"

	"github.com/ArronJablonowski/DarwinRouter/skills"
)

type SkillRegressionOperation = skills.RegressionOperation

// SkillRegressionOperation reads an existing historical receipt. Its After
// state does not prove current activation or authorize further mutation.
func (c *Client) SkillRegressionOperation(ctx context.Context, key skills.Key, operation string) (skills.RegressionOperation, error) {
	if !c.valid(ctx) {
		return skills.RegressionOperation{}, ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return skills.RegressionOperation{}, err
	}
	return c.service.SkillRegressionOperation(ctx, key, operation)
}

// RevalidateSkillVersionOnce requests one durable, operation-bound regression
// check. Exact retries acknowledge saved results without revalidation or another
// rollback. The host owns stable validator identity and deterministic, read-only,
// concurrent-safe, cancellation-cooperative callbacks; this is not a sandbox.
func (c *Client) RevalidateSkillVersionOnce(ctx context.Context, operation, validatorID string, expected skills.ActivationState, validator skills.Validator) (skills.RegressionOperation, error) {
	if !c.valid(ctx) {
		return skills.RegressionOperation{}, ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return skills.RegressionOperation{}, err
	}
	return c.service.RevalidateSkillVersionOnce(ctx, operation, validatorID, expected, validator)
}
