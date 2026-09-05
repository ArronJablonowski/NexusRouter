package telemetry

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

// verifySummarySource reads an immutable completed source before taking the
// writer lock. No completed task can accept additional events through Append.
func (s *Store) verifySummarySource(ctx context.Context, a sessions.SummaryAttempt) error {
	source, err := sessions.Replay(ctx, s, a.TaskID)
	if err != nil {
		return err
	}
	request := sessions.CompactionRequest{Keep: a.Keep, Summary: sessions.Summary{Decisions: []string{"pending summary validation"}}}
	if a.Draft != nil {
		request = a.Draft.Request
	}
	_, checkpoint, err := sessions.PrepareContinuation(source, request)
	if err != nil || checkpoint.SourceSequence != a.SourceSequence || checkpoint.SourceDigest != a.SourceDigest {
		return sessions.ErrHistory
	}
	if a.Draft != nil {
		want, _ := json.Marshal(checkpoint)
		got, _ := json.Marshal(a.Draft.Checkpoint)
		if !bytes.Equal(want, got) {
			return sessions.ErrHistory
		}
	}
	return nil
}

func (s *Store) BeginSummary(ctx context.Context, a sessions.SummaryAttempt) error {
	if a.Validate() != nil || a.Status != "started" {
		return sessions.ErrHistory
	}
	if err := s.verifySummarySource(ctx, a); err != nil {
		return err
	}
	a.StartedAt = a.StartedAt.UTC()
	body, err := json.Marshal(a)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "UPDATE task_heads SET sequence=sequence WHERE task_id=?", a.TaskID); err != nil {
		return err
	}
	var prior []byte
	err = tx.QueryRowContext(ctx, "SELECT body FROM summary_attempts WHERE id=?", a.ID).Scan(&prior)
	if err == nil {
		if !bytes.Equal(body, prior) {
			return ErrConflict
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO summary_attempts VALUES(?,?,?)", a.ID, a.TaskID, body); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) FinishSummary(ctx context.Context, a sessions.SummaryAttempt) error {
	if a.Status != "failed" {
		return sessions.ErrHistory
	}
	return s.finishSummary(ctx, a)
}

func (s *Store) CompleteSummary(ctx context.Context, a sessions.SummaryAttempt) error {
	if a.Validate() != nil || a.Status != "drafted" {
		return sessions.ErrHistory
	}
	if err := s.verifySummarySource(ctx, a); err != nil {
		return err
	}
	return s.finishSummary(ctx, a)
}

func (s *Store) finishSummary(ctx context.Context, a sessions.SummaryAttempt) error {
	if a.Validate() != nil {
		return sessions.ErrHistory
	}
	a.StartedAt, a.FinishedAt = a.StartedAt.UTC(), a.FinishedAt.UTC()
	body, err := json.Marshal(a)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "UPDATE task_heads SET sequence=sequence WHERE task_id=?", a.TaskID); err != nil {
		return err
	}
	var priorBody []byte
	var task string
	if err = tx.QueryRowContext(ctx, "SELECT task_id,body FROM summary_attempts WHERE id=?", a.ID).Scan(&task, &priorBody); err != nil {
		return err
	}
	prior, err := decodeSummaryAttempt(priorBody, a.ID, task)
	if err != nil {
		return err
	}
	if prior.Status != "started" {
		if !bytes.Equal(body, priorBody) {
			return ErrConflict
		}
		return tx.Commit()
	}
	if prior.TaskID != a.TaskID || prior.SourceSequence != a.SourceSequence || prior.SourceDigest != a.SourceDigest || prior.Keep != a.Keep || prior.Model != a.Model || prior.Provider != a.Provider || prior.EstimatedCost != a.EstimatedCost || !prior.StartedAt.Equal(a.StartedAt) {
		return ErrConflict
	}
	result, err := tx.ExecContext(ctx, "UPDATE summary_attempts SET body=? WHERE id=? AND body=?", body, a.ID, priorBody)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return ErrConflict
	}
	return tx.Commit()
}

func (s *Store) SummaryAttempt(ctx context.Context, id string) (sessions.SummaryAttempt, error) {
	var task string
	var body []byte
	if err := s.db.QueryRowContext(ctx, "SELECT task_id,body FROM summary_attempts WHERE id=?", id).Scan(&task, &body); err != nil {
		return sessions.SummaryAttempt{}, err
	}
	return decodeSummaryAttempt(body, id, task)
}

func (s *Store) ListSummaryAttempts(ctx context.Context, task, after string, limit int) ([]sessions.SummaryAttempt, error) {
	validOptionalLabel := func(value string) bool {
		return len(value) <= 128 && strings.TrimSpace(value) == value && utf8.ValidString(value) && !strings.ContainsFunc(value, unicode.IsControl)
	}
	if !validOptionalLabel(task) || !validOptionalLabel(after) || limit < 1 || limit > 100 {
		return nil, sessions.ErrHistory
	}
	query := "SELECT id,task_id,body FROM summary_attempts WHERE id>? ORDER BY id LIMIT ?"
	args := []any{after, limit}
	if task != "" {
		query = "SELECT id,task_id,body FROM summary_attempts WHERE task_id=? AND id>? ORDER BY id LIMIT ?"
		args = []any{task, after, limit}
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []sessions.SummaryAttempt{}
	for rows.Next() {
		var id, sourceTask string
		var body []byte
		if err := rows.Scan(&id, &sourceTask, &body); err != nil {
			return nil, err
		}
		a, err := decodeSummaryAttempt(body, id, sourceTask)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func decodeSummaryAttempt(body []byte, id, task string) (sessions.SummaryAttempt, error) {
	var a sessions.SummaryAttempt
	if json.Unmarshal(body, &a) != nil || a.Validate() != nil || a.ID != id || a.TaskID != task {
		return sessions.SummaryAttempt{}, sessions.ErrHistory
	}
	return a, nil
}
