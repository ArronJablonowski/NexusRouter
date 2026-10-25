package telemetry

import (
	"context"
	"github.com/ArronJablonowski/NexusRouter/sessions"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

// SubmissionInputKey returns content-free binding metadata for a proven task's
// submission. It is not an execution capability and never returns request bytes.
func (s *Store) SubmissionInputKey(ctx context.Context, id string) (string, error) {
	if !sessions.ValidEventPageID(id) {
		return "", submissions.ErrInvalid
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return "", e
	}
	defer tx.Rollback()
	if _, e = readSubmission(ctx, tx, id); e != nil {
		return "", e
	}
	var key string
	e = tx.QueryRowContext(ctx, `SELECT key_digest FROM submissions WHERE id=?`, id).Scan(&key)
	if e != nil {
		return "", e
	}
	if !submissionDigest(key) {
		return "", submissions.ErrInvalid
	}
	if e = tx.Commit(); e != nil {
		return "", e
	}
	return key, nil
}
