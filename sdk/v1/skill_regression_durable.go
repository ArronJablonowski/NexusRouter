package v1

import (
	"context"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

type SkillRegressionMonitorState = skills.RegressionMonitorState
type SkillRegressionMonitorCheck = skills.RegressionMonitorCheck

// DurableSkillRegressionStep uses a persisted named schedule. A returned error
// can accompany an advanced cursor with a failed check; cursor movement is not
// successful validation. Inspect retained checks to distinguish outcomes.
func (c *Client) DurableSkillRegressionStep(ctx context.Context, name, validatorID string, interval time.Duration, validator skills.Validator) (skills.RegressionMonitorState, error) {
	if !c.valid(ctx) || reservedSkillValidatorID(validatorID) {
		return skills.RegressionMonitorState{}, ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return skills.RegressionMonitorState{}, err
	}
	return c.service.DurableSkillRegressionStep(ctx, name, validatorID, interval, validator)
}

// SkillRegressionMonitorState reads existing scheduling metadata, not liveness.
func (c *Client) SkillRegressionMonitorState(ctx context.Context, name string) (skills.RegressionMonitorState, error) {
	if !c.valid(ctx) {
		return skills.RegressionMonitorState{}, ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return skills.RegressionMonitorState{}, err
	}
	return c.service.SkillRegressionMonitorState(ctx, name)
}

// SkillRegressionMonitorCheck reads a retained intent/outcome without dispatch.
func (c *Client) SkillRegressionMonitorCheck(ctx context.Context, name, operation string) (skills.RegressionMonitorCheck, error) {
	if !c.valid(ctx) {
		return skills.RegressionMonitorCheck{}, ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return skills.RegressionMonitorCheck{}, err
	}
	return c.service.SkillRegressionMonitorCheck(ctx, name, operation)
}

// StartDurableSkillRegression starts a caller-owned monitor with a stable name,
// validator contract identity and cadence. Call Close to cancel and join it.
// Validators are trusted, read-only, retry-safe and cooperative, not sandboxed.
func (c *Client) StartDurableSkillRegression(ctx context.Context, name, validatorID string, interval time.Duration, validator skills.Validator) (*SkillRegressionMonitor, error) {
	if !c.valid(ctx) || reservedSkillValidatorID(validatorID) {
		return nil, ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	monitor, err := app.StartDurableSkillRegression(ctx, c.service, name, validatorID, interval, validator)
	if err != nil {
		return nil, err
	}
	return &SkillRegressionMonitor{monitor: monitor}, nil
}
