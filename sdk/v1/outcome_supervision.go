package v1

import (
	"context"

	"github.com/ArronJablonowski/NexusRouter/health"
	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/skills"
)

type OutcomeRollbackCandidate = skills.OutcomeRollbackCandidate
type OutcomeRollbackReadiness = app.OutcomeRollbackReadiness
type OutcomeSupervisionState = skills.OutcomeSupervisionState
type OutcomeSupervisionCheck = skills.OutcomeSupervisionCheck

// OutcomeSupervisionMonitor owns an explicitly started configured supervisor.
// The caller must Close it to cancel and join its background operation.
type OutcomeSupervisionMonitor struct {
	monitor *app.OutcomeSupervisionMonitor
}

// ConfiguredOutcomeSupervision owns the configured supervisor lifecycle. A
// disabled policy returns a valid disabled handle so hosts can compose health
// and shutdown uniformly.
type ConfiguredOutcomeSupervision struct {
	supervisor *app.ConfiguredOutcomeSupervision
}

func (s *ConfiguredOutcomeSupervision) Close() error {
	if s == nil || s.supervisor == nil {
		return nil
	}
	return s.supervisor.Close()
}

func (s *ConfiguredOutcomeSupervision) Health() health.Check {
	if s == nil || s.supervisor == nil {
		return (*app.ConfiguredOutcomeSupervision)(nil).Health()
	}
	return s.supervisor.Health()
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

// DurableOutcomeSupervisionStep advances the configured persisted supervisor.
// Pending work is reconciled after restart without selecting another activation.
func (c *Client) DurableOutcomeSupervisionStep(ctx context.Context) (OutcomeSupervisionState, error) {
	if !c.valid(ctx) {
		return OutcomeSupervisionState{}, ErrAdmission
	}
	return c.service.DurableOutcomeSupervisionStep(ctx)
}

// OutcomeSupervisionState reads the configured durable cursor.
func (c *Client) OutcomeSupervisionState(ctx context.Context) (OutcomeSupervisionState, error) {
	if !c.valid(ctx) {
		return OutcomeSupervisionState{}, ErrAdmission
	}
	return c.service.OutcomeSupervisionState(ctx)
}

// OutcomeSupervisionCheck reads one durable check by its stable identifier.
func (c *Client) OutcomeSupervisionCheck(ctx context.Context, checkID string) (OutcomeSupervisionCheck, error) {
	if !c.valid(ctx) {
		return OutcomeSupervisionCheck{}, ErrAdmission
	}
	return c.service.OutcomeSupervisionCheck(ctx, checkID)
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

// StartConfiguredOutcomeSupervision freezes configured policy, performs
// read-only durable-store preflight, and starts supervision when enabled.
func (c *Client) StartConfiguredOutcomeSupervision(ctx context.Context) (*ConfiguredOutcomeSupervision, error) {
	if !c.valid(ctx) {
		return nil, ErrAdmission
	}
	plan, err := app.PrepareConfiguredOutcomeSupervision(c.service)
	if err != nil {
		return nil, ErrAdmission
	}
	supervisor, err := plan.Start(ctx)
	if err != nil {
		return nil, ErrAdmission
	}
	return &ConfiguredOutcomeSupervision{supervisor: supervisor}, nil
}
