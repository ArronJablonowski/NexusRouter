package v1

import (
	"context"

	"github.com/ArronJablonowski/NexusRouter/skills"
)

type SkillActivationOperation = skills.ActivationOperation

// SkillActivationOperation reads a durable receipt, not current activation or
// permission to repeat an operation.
func (c *Client) SkillActivationOperation(ctx context.Context, key skills.Key, operation string) (skills.ActivationOperation, error) {
	if !c.valid(ctx) {
		return skills.ActivationOperation{}, ErrAdmission
	}
	return c.service.SkillActivationOperation(ctx, key, operation)
}

// ActivateSkillVersionOnce retries an exact operation without revalidation or
// reactivation after an acknowledged commit. New operations require a trusted,
// read-only, retry-safe validator and explicit automatic-activation policy.
func (c *Client) ActivateSkillVersionOnce(ctx context.Context, operation string, expected skills.ActivationState, id string, validator skills.Validator) error {
	if !c.valid(ctx) {
		return ErrAdmission
	}
	return c.service.ActivateSkillVersionOnce(ctx, operation, expected, id, validator)
}
