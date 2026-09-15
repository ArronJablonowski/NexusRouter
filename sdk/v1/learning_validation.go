package v1

import (
	"context"

	"github.com/ArronJablonowski/DarwinRouter/health"
	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

type LearningActivationIntent = skills.LearningActivationIntent

// LearningSupervisor owns one explicitly started background learner. Its caller
// must Close it; Client itself does not own or stop these supervisors.
type LearningSupervisor struct{ learner *app.Learner }

func (l *LearningSupervisor) Close() error {
	if l == nil {
		return nil
	}
	return l.learner.Close()
}

func (l *LearningSupervisor) Health() health.Check {
	if l == nil {
		return (*app.Learner)(nil).Health()
	}
	return l.learner.Health()
}

// LearningStepWithValidation advances bounded learning with a named trusted,
// read-only, retry-safe validator. Model judgment is not acceptance evidence.
func (c *Client) LearningStepWithValidation(ctx context.Context, validatorID string, validator skills.Validator) (skills.LearningState, error) {
	if !c.valid(ctx) || reservedSkillValidatorID(validatorID) {
		return skills.LearningState{}, ErrAdmission
	}
	return c.service.LearningStepWithValidation(ctx, validatorID, validator)
}

// StartLearningWithValidation explicitly starts a cooperative in-process
// learner. The caller must retain and close the returned supervisor. Validator
// code is trusted, cancellation-cooperative host code, not a sandbox.
func (c *Client) StartLearningWithValidation(ctx context.Context, validatorID string, validator skills.Validator) (*LearningSupervisor, error) {
	if !c.valid(ctx) || reservedSkillValidatorID(validatorID) {
		return nil, ErrAdmission
	}
	learner, err := app.StartLearningWithValidation(ctx, c.service, validatorID, validator)
	if err != nil {
		return nil, err
	}
	return &LearningSupervisor{learner: learner}, nil
}

func (c *Client) SkillLearningActivationIntent(ctx context.Context, selectionID string) (skills.LearningActivationIntent, error) {
	if !c.valid(ctx) {
		return skills.LearningActivationIntent{}, ErrAdmission
	}
	return c.service.SkillLearningActivationIntent(ctx, selectionID)
}
