package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
)

func (s *Store) BeginReview(ctx context.Context, r evaluation.ReviewAttempt) error {
	_, _, err := s.admitReview(ctx, r, false)
	return err
}

// AdmitReview durably admits one public audit operation. Exact identity replays
// return the existing row with created=false; reuse of the operation ID with a
// changed request or reviewer fails closed.
func (s *Store) AdmitReview(ctx context.Context, r evaluation.ReviewAttempt) (evaluation.ReviewAttempt, bool, error) {
	return s.admitReview(ctx, r, true)
}

func (s *Store) admitReview(ctx context.Context, r evaluation.ReviewAttempt, public bool) (evaluation.ReviewAttempt, bool, error) {
	if r.Validate() != nil || r.Status != "started" {
		return evaluation.ReviewAttempt{}, false, evaluation.ErrAudit
	}
	if public && (r.ReviewerID == "" || r.RequestDigest == "") {
		return evaluation.ReviewAttempt{}, false, evaluation.ErrAudit
	}
	// Only identities crossing URL or caller-controlled operation boundaries
	// must be path-safe. Provider-native model names are already bounded by
	// ReviewAttempt.Validate and commonly contain ':' or '/'.
	if public && (!evaluation.ValidAuditOperationID(r.ID) || !evaluation.ValidAuditOperationID(r.TaskID) || !evaluation.ValidAuditOperationID(r.AttemptID) || !evaluation.ValidAuditOperationID(r.ReviewerID)) {
		return evaluation.ReviewAttempt{}, false, evaluation.ErrAudit
	}
	r.StartedAt = r.StartedAt.UTC()
	body, err := json.Marshal(r)
	if err != nil {
		return evaluation.ReviewAttempt{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return evaluation.ReviewAttempt{}, false, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "UPDATE task_heads SET sequence=sequence WHERE task_id=?", r.TaskID); err != nil {
		return evaluation.ReviewAttempt{}, false, err
	}
	var priorBody []byte
	var priorTask, priorStatus string
	err = tx.QueryRowContext(ctx, "SELECT task_id,status,body FROM review_attempts WHERE id=?", r.ID).Scan(&priorTask, &priorStatus, &priorBody)
	if err == nil {
		prior, decodeErr := decodeReviewAttempt(priorBody, r.ID, priorTask, priorStatus)
		if decodeErr != nil {
			return evaluation.ReviewAttempt{}, false, decodeErr
		}
		same := string(priorBody) == string(body)
		if public {
			same = prior.TaskID == r.TaskID && prior.AttemptID == r.AttemptID && prior.SourceKind == r.SourceKind && prior.EvaluatorModel == r.EvaluatorModel && prior.EvaluatorProvider == r.EvaluatorProvider && prior.ReviewerID == r.ReviewerID && prior.RequestDigest == r.RequestDigest && prior.EstimatedCost == r.EstimatedCost
		}
		if !same {
			return evaluation.ReviewAttempt{}, false, ErrConflict
		}
		if err := tx.Commit(); err != nil {
			return evaluation.ReviewAttempt{}, false, err
		}
		return prior, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return evaluation.ReviewAttempt{}, false, err
	}
	if err = validateAuditSource(ctx, tx, r.TaskID, r.AttemptID, r.SourceKind); err != nil {
		return evaluation.ReviewAttempt{}, false, err
	}

	if _, err = tx.ExecContext(ctx, "INSERT INTO review_attempts VALUES(?,?,?,?)", r.ID, r.TaskID, r.Status, body); err != nil {
		return evaluation.ReviewAttempt{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return evaluation.ReviewAttempt{}, false, err
	}
	return r, true, nil
}

func (s *Store) FinishReview(ctx context.Context, r evaluation.ReviewAttempt) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := finishReview(ctx, tx, r); err != nil {
		return err
	}
	if err := appendReviewUsage(ctx, tx, r); err != nil {
		return err
	}
	return tx.Commit()
}

// CompleteReview commits advisory evidence and its successful lifecycle together.
// A conflict or persistence error rolls back both changes. Exact retries are safe.
func (s *Store) CompleteReview(ctx context.Context, r evaluation.ReviewAttempt, a evaluation.AuditRecord) error {
	if r.Validate() != nil || a.Validate() != nil || r.Status != "completed" || r.AuditID != a.ID || r.TaskID != a.TaskID || r.AttemptID != a.AttemptID || r.SourceKind != a.SourceKind || r.EvaluatorModel != a.EvaluatorModel || r.EvaluatorProvider != a.EvaluatorProvider || (r.ReviewerID != "" && r.ReviewerID != a.Audit.EvaluatorID) {
		return evaluation.ErrAudit
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := recordAudit(ctx, tx, a); err != nil {
		return err
	}
	if err := finishReview(ctx, tx, r); err != nil {
		return err
	}
	if err := appendReviewUsage(ctx, tx, r); err != nil {
		return err
	}
	return tx.Commit()
}

func finishReview(ctx context.Context, tx *sql.Tx, r evaluation.ReviewAttempt) error {
	if r.Validate() != nil || r.Status == "started" {
		return evaluation.ErrAudit
	}
	r.StartedAt, r.FinishedAt = r.StartedAt.UTC(), r.FinishedAt.UTC()
	body, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE task_heads SET sequence=sequence WHERE task_id=?", r.TaskID); err != nil {
		return err
	}
	var priorBody []byte
	var task, status string
	if err = tx.QueryRowContext(ctx, "SELECT task_id,status,body FROM review_attempts WHERE id=?", r.ID).Scan(&task, &status, &priorBody); err != nil {
		return err
	}
	prior, err := decodeReviewAttempt(priorBody, r.ID, task, status)
	if err != nil {
		return err
	}
	if r.Status == "completed" {
		linked, linkErr := completedReviewForAudit(ctx, tx, r.TaskID, r.AuditID)
		if linkErr == nil && linked.ID != r.ID {
			return ErrConflict
		}
		if linkErr != nil && !errors.Is(linkErr, sql.ErrNoRows) {
			return linkErr
		}
	}
	if prior.Status != "started" {
		if string(priorBody) != string(body) {
			return ErrConflict
		}
		return nil
	}
	if prior.TaskID != r.TaskID || prior.AttemptID != r.AttemptID || prior.SourceKind != r.SourceKind || prior.EvaluatorModel != r.EvaluatorModel || prior.EvaluatorProvider != r.EvaluatorProvider || prior.ReviewerID != r.ReviewerID || prior.RequestDigest != r.RequestDigest || prior.EstimatedCost != r.EstimatedCost || !prior.StartedAt.Equal(r.StartedAt) {
		return ErrConflict
	}
	if r.Status == "completed" {
		var auditBody []byte
		var auditTask string
		if err = tx.QueryRowContext(ctx, "SELECT task_id,body FROM audit_records WHERE id=?", r.AuditID).Scan(&auditTask, &auditBody); err != nil {
			return err
		}
		a, err := decodeAuditRecord(auditBody, r.AuditID, auditTask)
		if err != nil {
			return err
		}
		if a.TaskID != r.TaskID || a.AttemptID != r.AttemptID || a.SourceKind != r.SourceKind || a.EvaluatorModel != r.EvaluatorModel || a.EvaluatorProvider != r.EvaluatorProvider || (r.ReviewerID != "" && a.Audit.EvaluatorID != r.ReviewerID) {
			return evaluation.ErrAudit
		}
	}
	result, err := tx.ExecContext(ctx, "UPDATE review_attempts SET status=?,body=? WHERE id=? AND status='started'", r.Status, body, r.ID)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return ErrConflict
	}
	return nil
}

// completedReviewForAudit resolves the exclusive durable owner of one audit
// evidence record. More than one match is corruption, never an arbitrary
// winner; callers use the task-head writer transaction to serialize competing
// completions before establishing this relationship.
func completedReviewForAudit(ctx context.Context, tx *sql.Tx, task, auditID string) (evaluation.ReviewAttempt, error) {
	rows, err := tx.QueryContext(ctx, "SELECT id,status,body FROM review_attempts WHERE task_id=? AND status='completed' AND json_extract(body,'$.AuditID')=? ORDER BY id LIMIT 2", task, auditID)
	if err != nil {
		return evaluation.ReviewAttempt{}, err
	}
	defer rows.Close()
	var out evaluation.ReviewAttempt
	count := 0
	for rows.Next() {
		var id, status string
		var body []byte
		if err := rows.Scan(&id, &status, &body); err != nil {
			return evaluation.ReviewAttempt{}, err
		}
		review, err := decodeReviewAttempt(body, id, task, status)
		if err != nil {
			return evaluation.ReviewAttempt{}, err
		}
		out, count = review, count+1
	}
	if err := rows.Err(); err != nil {
		return evaluation.ReviewAttempt{}, err
	}
	if count == 0 {
		return evaluation.ReviewAttempt{}, sql.ErrNoRows
	}
	if count != 1 {
		return evaluation.ReviewAttempt{}, evaluation.ErrAudit
	}
	return out, nil
}

// CancelReview is a cancellation-first compare-and-set. Once it commits, a
// concurrent completion cannot attach advisory evidence. Existing terminal
// state is returned unchanged, making repeated cancellation safe.
func (s *Store) CancelReview(ctx context.Context, task, id string, at time.Time) (evaluation.ReviewAttempt, error) {
	if !evaluation.ValidAuditOperationID(task) || !evaluation.ValidAuditOperationID(id) || at.IsZero() {
		return evaluation.ReviewAttempt{}, evaluation.ErrAudit
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return evaluation.ReviewAttempt{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "UPDATE task_heads SET sequence=sequence WHERE task_id=?", task); err != nil {
		return evaluation.ReviewAttempt{}, err
	}
	var storedTask, status string
	var body []byte
	if err = tx.QueryRowContext(ctx, "SELECT task_id,status,body FROM review_attempts WHERE id=?", id).Scan(&storedTask, &status, &body); err != nil {
		return evaluation.ReviewAttempt{}, err
	}
	prior, err := decodeReviewAttempt(body, id, storedTask, status)
	if err != nil {
		return evaluation.ReviewAttempt{}, err
	}
	if prior.TaskID != task {
		return evaluation.ReviewAttempt{}, sql.ErrNoRows
	}
	if prior.Status != "started" {
		if err := tx.Commit(); err != nil {
			return evaluation.ReviewAttempt{}, err
		}
		return prior, nil
	}
	canceled := prior
	canceled.Status, canceled.Code, canceled.FinishedAt = "failed", "canceled", at.UTC()
	if canceled.Validate() != nil {
		return evaluation.ReviewAttempt{}, evaluation.ErrAudit
	}
	terminalBody, err := json.Marshal(canceled)
	if err != nil {
		return evaluation.ReviewAttempt{}, err
	}
	result, err := tx.ExecContext(ctx, "UPDATE review_attempts SET status='failed',body=? WHERE id=? AND task_id=? AND status='started'", terminalBody, id, task)
	if err != nil {
		return evaluation.ReviewAttempt{}, err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return evaluation.ReviewAttempt{}, ErrConflict
	}
	if err := appendReviewUsage(ctx, tx, canceled); err != nil {
		return evaluation.ReviewAttempt{}, err
	}
	if err := tx.Commit(); err != nil {
		return evaluation.ReviewAttempt{}, err
	}
	return canceled, nil
}

func (s *Store) ReviewAttempt(ctx context.Context, id string) (evaluation.ReviewAttempt, error) {
	var body []byte
	var task, status string
	if err := s.db.QueryRowContext(ctx, "SELECT task_id,status,body FROM review_attempts WHERE id=?", id).Scan(&task, &status, &body); err != nil {
		return evaluation.ReviewAttempt{}, err
	}
	return decodeReviewAttempt(body, id, task, status)
}

func (s *Store) ReviewAttempts(ctx context.Context, task, afterID string, limit int) ([]evaluation.ReviewAttempt, error) {
	if task == "" || len(task) > 128 || len(afterID) > 128 || limit < 1 || limit > 100 {
		return nil, evaluation.ErrAudit
	}
	rows, err := s.db.QueryContext(ctx, "SELECT id,status,body FROM review_attempts WHERE task_id=? AND id>? ORDER BY id LIMIT ?", task, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []evaluation.ReviewAttempt{}
	for rows.Next() {
		var id, status string
		var body []byte
		if err := rows.Scan(&id, &status, &body); err != nil {
			return nil, err
		}
		r, err := decodeReviewAttempt(body, id, task, status)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func decodeReviewAttempt(body []byte, id, task, status string) (evaluation.ReviewAttempt, error) {
	var r evaluation.ReviewAttempt
	if json.Unmarshal(body, &r) != nil || r.Validate() != nil || r.ID != id || r.TaskID != task || r.Status != status {
		return evaluation.ReviewAttempt{}, evaluation.ErrAudit
	}
	return r, nil
}
