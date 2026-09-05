package v1

import (
	"context"

	"github.com/ArronJablonowski/DarwinRouter/skills"
)

// SkillActivationState reads the configured file catalog without initialization.
// It does not infer mutation authority from an injected SkillStore.
func (c *Client) SkillActivationState(ctx context.Context, key skills.Key) (skills.ActivationState, error) {
	if !c.valid(ctx) {
		return skills.ActivationState{}, ErrAdmission
	}
	return c.service.SkillActivationState(ctx, key)
}

// ActivateSkillVersion requires explicit activation configuration and a trusted,
// read-only, retry-safe deterministic validator. Validation is cooperative and
// in-process, not sandboxed; no model-supplied proof can replace the callback.
// The expected revision fences intervening transitions. Inspect state afterward.
func (c *Client) ActivateSkillVersion(ctx context.Context, expected skills.ActivationState, id string, validator skills.Validator) error {
	if !c.valid(ctx) {
		return ErrAdmission
	}
	return c.service.ActivateSkillVersion(ctx, expected, id, validator)
}
