package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
)

// RecordAudit stores reviewer provenance separately from candidate fitness.
// Exact retries are idempotent; changing an existing identity is a conflict.
func (s *Store) RecordAudit(ctx context.Context, r evaluation.AuditRecord) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := recordAudit(ctx, tx, r); err != nil {
		return err
	}
	return tx.Commit()
}

func recordAudit(ctx context.Context, tx *sql.Tx, r evaluation.AuditRecord) error {
	if err := r.Validate(); err != nil {
		return err
	}
	r.Time = r.Time.UTC()
	body, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE task_heads SET sequence=sequence WHERE task_id=?", r.TaskID); err != nil {
		return err
	}
	var prior []byte
	err = tx.QueryRowContext(ctx, "SELECT body FROM audit_records WHERE id=?", r.ID).Scan(&prior)
	if err == nil {
		if string(prior) != string(body) {
			return ErrConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err = validateAuditSource(ctx, tx, r.TaskID, r.AttemptID, r.SourceKind); err != nil {
		return err
	}

	if _, err = tx.ExecContext(ctx, "INSERT INTO audit_records(id,task_id,body) VALUES(?,?,?)", r.ID, r.TaskID, body); err != nil {
		return err
	}
	return nil
}

func (s *Store) Audit(ctx context.Context, id string) (evaluation.AuditRecord, error) {
	var task string
	var body []byte
	if err := s.db.QueryRowContext(ctx, "SELECT task_id,body FROM audit_records WHERE id=?", id).Scan(&task, &body); err != nil {
		return evaluation.AuditRecord{}, err
	}
	return decodeAuditRecord(body, id, task)
}

// Audits pages one task's immutable audit records in ascending ID order.
func (s *Store) Audits(ctx context.Context, task, afterID string, limit int) ([]evaluation.AuditRecord, error) {
	if task == "" || len(task) > 128 || len(afterID) > 128 || limit < 1 || limit > 100 {
		return nil, evaluation.ErrAudit
	}
	rows, err := s.db.QueryContext(ctx, "SELECT id,body FROM audit_records WHERE task_id=? AND id>? ORDER BY id LIMIT ?", task, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []evaluation.AuditRecord{}
	for rows.Next() {
		var id string
		var body []byte
		if err := rows.Scan(&id, &body); err != nil {
			return nil, err
		}
		r, err := decodeAuditRecord(body, id, task)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func decodeAuditRecord(body []byte, id, task string) (evaluation.AuditRecord, error) {
	var r evaluation.AuditRecord
	if json.Unmarshal(body, &r) != nil || r.ID != id || r.TaskID != task || r.Validate() != nil {
		return evaluation.AuditRecord{}, evaluation.ErrAudit
	}
	return r, nil
}
