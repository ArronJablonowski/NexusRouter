package app

import (
	"context"
	"errors"
	"time"

	"github.com/ArronJablonowski/NexusRouter/approvals"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
)

// DecideApproval requires a host-authenticated actor and the exact inspected
// request. It records authority only; execution remains owned by the waiting
// runtime and cannot be resumed or retried through this control.
func (s *Service) DecideApproval(ctx context.Context, command approvals.Command, actor string) (approvals.Record, error) {
	if s == nil || ctx == nil || command.Validate() != nil {
		return approvals.Record{}, ErrAdmission
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if ctx.Err() != nil {
		return approvals.Record{}, ctx.Err()
	}
	guard := toolAuthority{secrets: memorySecrets(s.settings, s.secret)}
	r := command.Expected
	for _, identity := range []string{actor, command.ID, r.ID, r.TaskID, r.TurnID, r.ToolCallID, r.ToolName, r.Scope} {
		if guard.containsSecret(identity) {
			return approvals.Record{}, ErrAdmission
		}
	}
	if (approvals.Decision{ID: command.ID, Actor: actor, Time: time.Now().UTC()}).Validate() != nil {
		return approvals.Record{}, ErrAdmission
	}
	db, err := telemetry.OpenApprovalControl(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return approvals.Record{}, approvals.ErrUnavailable
	}
	defer db.Close()
	result, err := db.DecideBoundApproval(ctx, command, actor, time.Now().UTC())
	if err != nil {
		for _, known := range []error{approvals.ErrInvalid, approvals.ErrConflict, context.Canceled, context.DeadlineExceeded} {
			if errors.Is(err, known) {
				return approvals.Record{}, known
			}
		}
		return approvals.Record{}, approvals.ErrUnavailable
	}
	if result.Validate() != nil || !result.Request.Matches(command.Expected) {
		return approvals.Record{}, approvals.ErrInvalid
	}
	return result, nil
}
