package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
)

func (s *Store) BeginReview(ctx context.Context, r evaluation.ReviewAttempt) error {
	if r.Validate() != nil || r.Status != "started" {
		return evaluation.ErrAudit
	}
	r.StartedAt = r.StartedAt.UTC()
	body, err := json.Marshal(r)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "UPDATE task_heads SET sequence=sequence WHERE task_id=?", r.TaskID); err != nil {
		return err
	}
	var prior []byte
	err = tx.QueryRowContext(ctx, "SELECT body FROM review_attempts WHERE id=?", r.ID).Scan(&prior)
	if err == nil {
		if string(prior) != string(body) {
			return ErrConflict
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var started, ended, terminal int
	err = tx.QueryRowContext(ctx, `SELECT
	 (SELECT count(*) FROM events WHERE task_id=? AND json_extract(body,'$.attempt_id')=? AND json_extract(body,'$.kind')='turn.started'),
	 (SELECT count(*) FROM events WHERE task_id=? AND json_extract(body,'$.attempt_id')=? AND json_extract(body,'$.kind')='turn.completed'),
	 (SELECT count(*) FROM task_heads WHERE task_id=? AND state IN ('completed','failed'))`, r.TaskID, r.AttemptID, r.TaskID, r.AttemptID, r.TaskID).Scan(&started, &ended, &terminal)
	if err != nil {
		return err
	}
	if started != 1 || ended != 1 || terminal != 1 {
		return evaluation.ErrAudit
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO review_attempts VALUES(?,?,?,?)", r.ID, r.TaskID, r.Status, body); err != nil {
		return err
	}
	return tx.Commit()
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
	return tx.Commit()
}

// CompleteReview commits advisory evidence and its successful lifecycle together.
// A conflict or persistence error rolls back both changes. Exact retries are safe.
func (s *Store) CompleteReview(ctx context.Context, r evaluation.ReviewAttempt, a evaluation.AuditRecord) error {
	if r.Validate() != nil || a.Validate() != nil || r.Status != "completed" || r.AuditID != a.ID || r.TaskID != a.TaskID || r.AttemptID != a.AttemptID || r.EvaluatorModel != a.EvaluatorModel || r.EvaluatorProvider != a.EvaluatorProvider {
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
	if prior.Status != "started" {
		if string(priorBody) != string(body) {
			return ErrConflict
		}
		return nil
	}
	if prior.TaskID != r.TaskID || prior.AttemptID != r.AttemptID || prior.EvaluatorModel != r.EvaluatorModel || prior.EvaluatorProvider != r.EvaluatorProvider || !prior.StartedAt.Equal(r.StartedAt) {
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
		if a.TaskID != r.TaskID || a.AttemptID != r.AttemptID || a.EvaluatorModel != r.EvaluatorModel || a.EvaluatorProvider != r.EvaluatorProvider {
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
