package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/ArronJablonowski/NexusRouter/approvals"
)

// DecideBoundApproval binds a server-attributed operator command to the exact
// observed request before considering idempotency. Retried commands retain the
// original timestamp and return current state, never resurrect spent authority.
func (s *Store) DecideBoundApproval(ctx context.Context, command approvals.Command, actor string, now time.Time) (approvals.Record, error) {
	d := approvals.Decision{ID: command.ID, Actor: actor, Allowed: command.Allowed, Time: now}
	if command.Validate() != nil || d.Validate() != nil {
		return approvals.Record{}, approvals.ErrInvalid
	}
	tx, err := s.approvalTx(ctx)
	if err != nil {
		return approvals.Record{}, err
	}
	defer tx.Rollback()
	r, err := readApproval(ctx, tx, command.Expected.ID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = approvals.ErrUnavailable
		}
		return approvals.Record{}, err
	}
	if !r.Request.Matches(command.Expected) {
		return approvals.Record{}, approvals.ErrConflict
	}
	for _, prior := range r.Decisions {
		if prior.ID == command.ID {
			if prior.Actor != actor || prior.Allowed != command.Allowed {
				return approvals.Record{}, approvals.ErrConflict
			}
			return commitApproval(tx, r)
		}
	}
	if now.Before(r.Request.CreatedAt) || !now.Before(r.Request.ExpiresAt) {
		return approvals.Record{}, approvals.ErrConflict
	}
	if len(r.Decisions) > 0 && now.Before(r.Decisions[len(r.Decisions)-1].Time) {
		return approvals.Record{}, approvals.ErrConflict
	}
	if err = approvalCall(ctx, tx, r.Request); err != nil {
		return approvals.Record{}, err
	}
	switch {
	case r.State == approvals.Pending && d.Allowed:
		r.State = approvals.Approved
	case r.State == approvals.Pending && !d.Allowed:
		r.State = approvals.Denied
	case r.State == approvals.Approved && !d.Allowed:
		r.State = approvals.Revoked
	default:
		return approvals.Record{}, approvals.ErrConflict
	}
	r.Decisions = append(r.Decisions, d)
	if err = saveApproval(ctx, tx, r, false); err != nil {
		return approvals.Record{}, err
	}
	return commitApproval(tx, r)
}
