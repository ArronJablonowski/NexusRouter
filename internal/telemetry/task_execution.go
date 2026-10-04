package telemetry

import (
	"context"
	"database/sql"
	"github.com/ArronJablonowski/NexusRouter/sessions"
	"time"
)

// ObserveTaskExecution joins durable submission outcomes and current lease evidence.
// It never infers a successful outcome from elapsed time or changes any history.
func (s *Store) ObserveTaskExecution(ctx context.Context, page *sessions.TaskPage, now time.Time) error {
	if page == nil || page.Validate() != nil {
		return sessions.ErrTaskList
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i := range page.Items {
		item := &page.Items[i]
		var head, submission, expiry string
		var active, expired int
		err = tx.QueryRowContext(ctx, `SELECT h.state,COALESCE(s.state,''),COALESCE(s.lease_expires_at,''),
   (SELECT count(*) FROM resource_leases l WHERE l.task_id=h.task_id AND l.released=0 AND l.expires>?),
   (SELECT count(*) FROM resource_leases l WHERE l.task_id=h.task_id AND l.released=0 AND l.expires<=?)
   FROM task_heads h LEFT JOIN events e ON e.task_id=h.task_id AND e.sequence=1
   LEFT JOIN submissions s ON s.id=json_extract(e.body,'$.data.submission_id') WHERE h.task_id=?`, now.UnixNano(), now.UnixNano(), item.TaskID).Scan(&head, &submission, &expiry, &active, &expired)
		if err != nil {
			return err
		}
		result := sessions.TaskExecution{State: "unknown", Evidence: "no_active_execution_lease", ObservedAt: now.UTC()}
		switch {
		case head == "completed" || head == "failed" || head == "canceled":
			result.State = head
			result.Evidence = "task_terminal_event"
		case submission == "failed" || submission == "canceled" || submission == "succeeded":
			result.State = submission
			result.Evidence = "submission_terminal_state"
		case active > 0:
			result.State = "running"
			result.Evidence = "active_execution_lease"
		case submission == "running":
			expires, e := time.Parse(time.RFC3339Nano, expiry)
			if e == nil && expires.After(now) {
				result.State = "running"
				result.Evidence = "active_submission_lease"
			} else {
				result.Evidence = "expired_submission_lease"
			}
		case expired > 0:
			result.Evidence = "expired_unreleased_lease"
		}
		item.Execution = &result
	}
	return tx.Commit()
}
