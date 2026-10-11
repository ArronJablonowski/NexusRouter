package remote

import (
	"context"
	"database/sql"
	"errors"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

// ErrNotAdmitted means an exact reserved request has no durable submission at
// reconciliation time. It never authorizes a different payload or request key.
var ErrNotAdmitted = errors.New("remote request not admitted; retain exact request identity")

type submissionRecovery interface {
	RecoverSubmission(context.Context, string, Task) (submissions.Status, error)
}

// RecoverStatus reads the exact original request and repairs only its ownership
// binding. It cannot submit work, run inference, or cancel an existing task.
func (c *Client) RecoverStatus(ctx context.Context, destination, key string, task Task) (submissions.Status, error) {
	var out submissions.Status
	if !requestID(key) || task.Validate() != nil {
		return out, ErrInvalid
	}
	err := c.call(ctx, destination, "inspect", "POST", "/v1/remote/tasks/"+key+"/recover", &task, nil, &out)
	return out, err
}

func (s *Server) recoverSubmission(ctx context.Context, caller, key string, task Task) (submissions.Status, error) {
	var digest, bound string
	err := s.journal.db.QueryRowContext(ctx, "SELECT digest,submission FROM requests WHERE caller=? AND request_id=?", caller, key).Scan(&digest, &bound)
	if err != nil {
		return submissions.Status{}, ErrUnavailable
	}
	if digest != hash(task) {
		return submissions.Status{}, ErrConflict
	}
	if bound != "" {
		return s.backend.Status(ctx, bound)
	}
	backend, ok := s.backend.(submissionRecovery)
	if !ok {
		return submissions.Status{}, ErrUnavailable
	}
	status, err := backend.RecoverSubmission(ctx, hash([]string{s.instance, caller, key}), task)
	if errors.Is(err, sql.ErrNoRows) {
		return submissions.Status{}, ErrNotAdmitted
	}
	if err != nil {
		return submissions.Status{}, err
	}
	if status.ID == "" {
		return submissions.Status{}, ErrUnavailable
	}
	// Binding is an idempotent control-plane repair, never a dispatch.
	if err = s.journal.bind(ctx, caller, key, status.ID); err != nil {
		return submissions.Status{}, err
	}
	return status, nil
}
