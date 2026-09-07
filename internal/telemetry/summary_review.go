package telemetry

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func (s *Store) RecordSummaryReview(ctx context.Context, r sessions.SummaryReview) error {
	if r.Validate() != nil {
		return sessions.ErrHistory
	}
	r.Time = r.Time.UTC()
	body, err := json.Marshal(r)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "UPDATE summary_attempts SET body=body WHERE id=?", r.AttemptID); err != nil {
		return err
	}
	var prior []byte
	err = tx.QueryRowContext(ctx, "SELECT body FROM summary_reviews WHERE id=?", r.ID).Scan(&prior)
	if err == nil {
		if !bytes.Equal(body, prior) {
			return ErrConflict
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var task string
	var attemptBody []byte
	if err = tx.QueryRowContext(ctx, "SELECT task_id,body FROM summary_attempts WHERE id=?", r.AttemptID).Scan(&task, &attemptBody); err != nil {
		return err
	}
	a, err := decodeSummaryAttempt(attemptBody, r.AttemptID, task)
	if err != nil {
		return err
	}
	if a.Status != "drafted" {
		return sessions.ErrHistory
	}
	var head string
	err = tx.QueryRowContext(ctx, "SELECT review_id FROM summary_review_heads WHERE attempt_id=?", r.AttemptID).Scan(&head)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if head != r.PreviousID {
		return ErrConflict
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM summary_reviews WHERE attempt_id=?", r.AttemptID).Scan(&count); err != nil {
		return err
	}
	if count >= 100 {
		return sessions.ErrHistory
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO summary_reviews VALUES(?,?,?)", r.ID, r.AttemptID, body); err != nil {
		return err
	}
	if head == "" {
		_, err = tx.ExecContext(ctx, "INSERT INTO summary_review_heads VALUES(?,?)", r.AttemptID, r.ID)
	} else {
		var result sql.Result
		result, err = tx.ExecContext(ctx, "UPDATE summary_review_heads SET review_id=? WHERE attempt_id=? AND review_id=?", r.ID, r.AttemptID, head)
		if err == nil {
			if n, e := result.RowsAffected(); e != nil || n != 1 {
				return ErrConflict
			}
		}
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CurrentSummaryReview(ctx context.Context, attempt string) (sessions.SummaryReview, error) {
	var id string
	var body []byte
	if err := s.db.QueryRowContext(ctx, "SELECT r.id,r.body FROM summary_review_heads h JOIN summary_reviews r ON r.id=h.review_id AND r.attempt_id=h.attempt_id WHERE h.attempt_id=?", attempt).Scan(&id, &body); err != nil {
		return sessions.SummaryReview{}, err
	}
	return decodeSummaryReview(body, id, attempt)
}

// LatestApprovedSummary returns the most recently recorded summary whose
// current review head is still approved for task. Revoked heads are skipped;
// malformed durable records fail closed instead of exposing an older draft.
// The search is deliberately bounded so admission cannot be made to scan an
// unbounded operator-review history.
func (s *Store) LatestApprovedSummary(ctx context.Context, task string) (sessions.SummaryAttempt, sessions.SummaryReview, error) {
	if ctx == nil || !sessions.ValidEventPageID(task) {
		return sessions.SummaryAttempt{}, sessions.SummaryReview{}, sessions.ErrHistory
	}
	rows, err := s.db.QueryContext(ctx, `SELECT a.id,
		CASE WHEN length(CAST(a.body AS BLOB)) BETWEEN 1 AND 1048576 THEN a.body END,
		r.id,CASE WHEN length(CAST(r.body AS BLOB)) BETWEEN 1 AND 16384 THEN r.body END
		FROM summary_attempts a
		JOIN summary_review_heads h ON h.attempt_id=a.id
		JOIN summary_reviews r ON r.id=h.review_id AND r.attempt_id=a.id
		WHERE a.task_id=? ORDER BY r.rowid DESC LIMIT 101`, task)
	if err != nil {
		return sessions.SummaryAttempt{}, sessions.SummaryReview{}, err
	}
	defer rows.Close()
	for count := 0; rows.Next(); count++ {
		if count == 100 {
			return sessions.SummaryAttempt{}, sessions.SummaryReview{}, sessions.ErrHistory
		}
		var attemptID, reviewID string
		var attemptBody, reviewBody []byte
		if err := rows.Scan(&attemptID, &attemptBody, &reviewID, &reviewBody); err != nil {
			return sessions.SummaryAttempt{}, sessions.SummaryReview{}, err
		}
		a, err := decodeSummaryAttempt(attemptBody, attemptID, task)
		if err != nil || a.Status != "drafted" || a.Draft == nil {
			return sessions.SummaryAttempt{}, sessions.SummaryReview{}, sessions.ErrHistory
		}
		r, err := decodeSummaryReview(reviewBody, reviewID, attemptID)
		if err != nil {
			return sessions.SummaryAttempt{}, sessions.SummaryReview{}, sessions.ErrHistory
		}
		if r.Decision == "approved" {
			return a, r, nil
		}
	}
	if err := rows.Err(); err != nil {
		return sessions.SummaryAttempt{}, sessions.SummaryReview{}, err
	}
	return sessions.SummaryAttempt{}, sessions.SummaryReview{}, sql.ErrNoRows
}

func (s *Store) SummaryReviews(ctx context.Context, attempt string) ([]sessions.SummaryReview, error) {
	if attempt == "" || len(attempt) > 128 {
		return nil, sessions.ErrHistory
	}
	rows, err := s.db.QueryContext(ctx, "SELECT id,body FROM summary_reviews WHERE attempt_id=? ORDER BY rowid LIMIT 101", attempt)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []sessions.SummaryReview{}
	previous := ""
	for rows.Next() {
		var id string
		var body []byte
		if err := rows.Scan(&id, &body); err != nil {
			return nil, err
		}
		r, err := decodeSummaryReview(body, id, attempt)
		if err != nil {
			return nil, err
		}
		if len(out) >= 100 || r.PreviousID != previous {
			return nil, sessions.ErrHistory
		}
		previous = r.ID
		out = append(out, r)
	}
	return out, rows.Err()
}

func decodeSummaryReview(body []byte, id, attempt string) (sessions.SummaryReview, error) {
	var r sessions.SummaryReview
	if json.Unmarshal(body, &r) != nil || r.Validate() != nil || r.ID != id || r.AttemptID != attempt {
		return sessions.SummaryReview{}, sessions.ErrHistory
	}
	return r, nil
}

// Called inside Append's writer transaction, after exact event retries have
// been handled. A concurrent revocation cannot slip between this check and the
// durable task start that permits provider dispatch.
func validateSummaryGate(ctx context.Context, tx *sql.Tx, c *runtime.ContextCompaction) error {
	var reviewBody, attemptBody []byte
	var reviewID, task string
	err := tx.QueryRowContext(ctx, `SELECT h.review_id,r.body,a.task_id,a.body FROM summary_review_heads h JOIN summary_reviews r ON r.id=h.review_id AND r.attempt_id=h.attempt_id JOIN summary_attempts a ON a.id=h.attempt_id WHERE h.attempt_id=?`, c.SummaryAttemptID).Scan(&reviewID, &reviewBody, &task, &attemptBody)
	if err != nil {
		return err
	}
	r, err := decodeSummaryReview(reviewBody, reviewID, c.SummaryAttemptID)
	if err != nil {
		return err
	}
	if r.Decision != "approved" || reviewID != c.SummaryReviewID {
		return ErrConflict
	}
	a, err := decodeSummaryAttempt(attemptBody, c.SummaryAttemptID, task)
	if err != nil {
		return err
	}
	if a.Status != "drafted" || task != c.SourceTaskID {
		return sessions.ErrHistory
	}
	clean := *c
	clean.SummaryAttemptID, clean.SummaryReviewID = "", ""
	want, err := json.Marshal(a.Draft.Checkpoint)
	if err != nil {
		return err
	}
	got, err := json.Marshal(clean)
	if err != nil {
		return err
	}
	if !bytes.Equal(want, got) {
		return sessions.ErrHistory
	}
	return nil
}
