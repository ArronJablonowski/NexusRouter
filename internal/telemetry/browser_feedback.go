package telemetry

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

const MaxBrowserFeedbackRevisions = 100

var ErrBrowserFeedback = errors.New("invalid browser feedback history")

type BrowserFeedback struct {
	Version     int       `json:"version"`
	ID          string    `json:"id"`
	TaskID      string    `json:"task_id"`
	Supersedes  string    `json:"supersedes,omitempty"`
	Accepted    bool      `json:"accepted"`
	AttemptCost float64   `json:"attempt_cost"`
	CreatedAt   time.Time `json:"created_at"`
}

func (r BrowserFeedback) Valid() bool {
	return r.Version == 1 && sessions.ValidEventPageID(r.ID) && sessions.ValidEventPageID(r.TaskID) &&
		(r.Supersedes == "" || sessions.ValidEventPageID(r.Supersedes)) && r.ID != r.Supersedes &&
		!math.IsNaN(r.AttemptCost) && !math.IsInf(r.AttemptCost, 0) && r.AttemptCost >= 0 &&
		!r.CreatedAt.IsZero() && r.CreatedAt.Equal(r.CreatedAt.UTC())
}

func (s *Store) AppendBrowserFeedback(ctx context.Context, record BrowserFeedback, expectedRevision int64) error {
	if s == nil || ctx == nil || !record.Valid() || expectedRevision < 0 || expectedRevision >= MaxBrowserFeedbackRevisions {
		return ErrBrowserFeedback
	}
	body, err := json.Marshal(record)
	if err != nil || len(body) > 4096 {
		return ErrBrowserFeedback
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var taskState string
	if err = tx.QueryRowContext(ctx, "SELECT state FROM task_heads WHERE task_id=?", record.TaskID).Scan(&taskState); err != nil || taskState != "completed" {
		return ErrBrowserFeedback
	}
	if existing, readErr := readBrowserFeedback(ctx, tx, record.ID); readErr == nil {
		if existing.TaskID == record.TaskID && existing.Supersedes == record.Supersedes && existing.Accepted == record.Accepted && existing.AttemptCost == record.AttemptCost {
			return tx.Commit()
		}
		return ErrBrowserOperationConflict
	} else if !errors.Is(readErr, sql.ErrNoRows) {
		return readErr
	}
	history, err := browserFeedbackHistory(ctx, tx, record.TaskID)
	if err != nil || int64(len(history)) != expectedRevision {
		if err == nil {
			return ErrBrowserOperationConflict
		}
		return err
	}
	if len(history) == 0 && record.Supersedes != "" || len(history) > 0 && (record.Supersedes != history[len(history)-1].ID || !record.CreatedAt.After(history[len(history)-1].CreatedAt)) {
		return ErrBrowserOperationConflict
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO browser_feedback(id,task_id,supersedes,accepted,attempt_cost,created_at,body) VALUES(?,?,?,?,?,?,?)`,
		record.ID, record.TaskID, nullableString(record.Supersedes), record.Accepted, record.AttemptCost, record.CreatedAt.UnixNano(), body)
	if err != nil {
		return ErrBrowserOperationConflict
	}
	return tx.Commit()
}

func (s *Store) BrowserFeedbackHistory(ctx context.Context, task string) ([]BrowserFeedback, error) {
	if s == nil || ctx == nil || !sessions.ValidEventPageID(task) {
		return nil, ErrBrowserFeedback
	}
	return browserFeedbackHistory(ctx, s.db, task)
}

func browserFeedbackHistory(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, task string) ([]BrowserFeedback, error) {
	rows, err := q.QueryContext(ctx, `SELECT id,task_id,COALESCE(supersedes,''),accepted,attempt_cost,created_at,body FROM browser_feedback WHERE task_id=? ORDER BY created_at,id LIMIT ?`, task, MaxBrowserFeedbackRevisions+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]BrowserFeedback, 0)
	for rows.Next() {
		var r BrowserFeedback
		var accepted bool
		var created int64
		var body []byte
		if err = rows.Scan(&r.ID, &r.TaskID, &r.Supersedes, &accepted, &r.AttemptCost, &created, &body); err != nil {
			return nil, err
		}
		r.Version, r.Accepted = 1, accepted
		r.CreatedAt = time.Unix(0, created).UTC()
		var encoded BrowserFeedback
		canonical, marshalErr := json.Marshal(r)
		if marshalErr != nil || json.Unmarshal(body, &encoded) != nil || !bytes.Equal(body, canonical) || encoded != r || !r.Valid() {
			return nil, ErrBrowserFeedback
		}
		if len(out) == 0 && r.Supersedes != "" || len(out) > 0 && r.Supersedes != out[len(out)-1].ID {
			return nil, ErrBrowserFeedback
		}
		out = append(out, r)
	}
	if err = rows.Err(); err != nil || len(out) > MaxBrowserFeedbackRevisions {
		return nil, ErrBrowserFeedback
	}
	return out, nil
}

func readBrowserFeedback(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (BrowserFeedback, error) {
	var r BrowserFeedback
	var accepted bool
	var created int64
	err := q.QueryRowContext(ctx, `SELECT id,task_id,COALESCE(supersedes,''),accepted,attempt_cost,created_at FROM browser_feedback WHERE id=?`, id).Scan(&r.ID, &r.TaskID, &r.Supersedes, &accepted, &r.AttemptCost, &created)
	if err != nil {
		return BrowserFeedback{}, err
	}
	r.Version, r.Accepted = 1, accepted
	r.CreatedAt = time.Unix(0, created).UTC()
	if !r.Valid() {
		return BrowserFeedback{}, ErrBrowserFeedback
	}
	return r, nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
