package v1

import (
	"context"

	"github.com/ArronJablonowski/DarwinRouter/health"
	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

type OutcomeRollbackCandidate = skills.OutcomeRollbackCandidate
type OutcomeRollbackReadiness = app.OutcomeRollbackReadiness

// OutcomeSupervisionMonitor owns an explicitly started configured supervisor.
// The caller must Close it to cancel and join its background operation.
type OutcomeSupervisionMonitor struct {
	monitor *app.OutcomeSupervisionMonitor
}

func (m *OutcomeSupervisionMonitor) Close() error {
	if m == nil {
		return nil
	}
	return m.monitor.Close()
}

func (m *OutcomeSupervisionMonitor) Health() health.Check {
	if m == nil {
		return (*app.OutcomeSupervisionMonitor)(nil).Health()
	}
	return m.monitor.Health()
}

// OutcomeRollbackCandidate inspects the exact current activation and immediate
// validated predecessor. It does not create or repair catalog state.
func (c *Client) OutcomeRollbackCandidate(ctx context.Context, key skills.Key) (OutcomeRollbackCandidate, error) {
	if !c.valid(ctx) {
		return OutcomeRollbackCandidate{}, ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return OutcomeRollbackCandidate{}, err
	}
	return c.service.OutcomeRollbackCandidate(ctx, key)
}

// InspectOutcomeRollbackReadiness selects a fresh bounded evidence snapshot.
// Waiting is a successful read-only result and creates no durable intent.
func (c *Client) InspectOutcomeRollbackReadiness(ctx context.Context, key skills.Key) (OutcomeRollbackReadiness, error) {
	if !c.valid(ctx) {
		return OutcomeRollbackReadiness{}, ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return OutcomeRollbackReadiness{}, err
	}
	return c.service.InspectOutcomeRollbackReadiness(ctx, key)
}

// OutcomeSupervisionStep advances one configured lexical scan step. It prepares
// an outcome action only after the returned evidence is decision-ready.
func (c *Client) OutcomeSupervisionStep(ctx context.Context, after string) (string, OutcomeRollbackReadiness, error) {
	if !c.valid(ctx) {
		return after, OutcomeRollbackReadiness{}, ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return after, OutcomeRollbackReadiness{}, err
	}
	return c.service.OutcomeSupervisionStep(ctx, after)
}

// StartOutcomeSupervision starts one immediate configured scan step and then
// repeats at the configured interval. The caller owns the returned monitor.
func (c *Client) StartOutcomeSupervision(ctx context.Context) (*OutcomeSupervisionMonitor, error) {
	if !c.valid(ctx) {
		return nil, ErrAdmission
	}
	monitor, err := app.StartOutcomeSupervision(ctx, c.service)
	if err != nil {
		return nil, err
	}
	return &OutcomeSupervisionMonitor{monitor: monitor}, nil
}
