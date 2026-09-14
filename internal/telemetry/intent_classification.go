package telemetry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/ArronJablonowski/DarwinRouter/classification"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

// BeginIntentClassification persists the started state before a provider call.
// An exact retry is acknowledgement-safe. task_id intentionally need not name
// an existing task head because classification can precede route selection and
// task journal creation.
func (s *Store) BeginIntentClassification(ctx context.Context, attempt classification.Attempt) error {
	if attempt.Status != classification.AttemptStarted || attempt.Validate() != nil {
		return classification.ErrInvalidInput
	}
	attempt.StartedAt = attempt.StartedAt.UTC()
	body, err := json.Marshal(attempt)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Acquire the SQLite writer reservation before identity discovery so two
	// processes cannot both conclude that a binding is absent.
	if _, err = tx.ExecContext(ctx, `UPDATE intent_classification_attempts SET status=status WHERE id=?`, attempt.ID); err != nil {
		return err
	}
	var prior []byte
	err = tx.QueryRowContext(ctx, `SELECT body FROM intent_classification_attempts WHERE id=?`, attempt.ID).Scan(&prior)
	if err == nil {
		if !bytes.Equal(prior, body) {
			return ErrConflict
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if attempt.SubmissionID != "" {
		var existing string
		err = tx.QueryRowContext(ctx, `SELECT id FROM intent_classification_attempts
			WHERE submission_id=?`, attempt.SubmissionID).Scan(&existing)
		if err == nil {
			return ErrConflict
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	var taskAttempt string
	err = tx.QueryRowContext(ctx, `SELECT id FROM intent_classification_attempts WHERE task_id=?`, attempt.TaskID).Scan(&taskAttempt)
	if err == nil {
		return ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO intent_classification_attempts
		(id,task_id,session_id,submission_id,status,body) VALUES(?,?,?,?,?,?)`,
		attempt.ID, attempt.TaskID, attempt.SessionID, attempt.SubmissionID, attempt.Status, body)
	if err != nil {
		// A concurrent task/submission winner is an identity conflict, not an
		// implementation-specific SQLite constraint error.
		if attempt.SubmissionID != "" {
			var existing string
			if queryErr := tx.QueryRowContext(ctx, `SELECT id FROM intent_classification_attempts
				WHERE submission_id=?`, attempt.SubmissionID).Scan(&existing); queryErr == nil {
				return ErrConflict
			}
		}
		if queryErr := tx.QueryRowContext(ctx, `SELECT id FROM intent_classification_attempts
			WHERE task_id=?`, attempt.TaskID).Scan(&taskAttempt); queryErr == nil {
			return ErrConflict
		}
		return err
	}
	return tx.Commit()
}

// FinishIntentClassification moves one started attempt to exactly one valid
// terminal state. Conflicting terminal outcomes cannot overwrite each other;
// an exact retry remains safe after acknowledgement loss.
func (s *Store) FinishIntentClassification(ctx context.Context, attempt classification.Attempt) error {
	if attempt.Status == classification.AttemptStarted || attempt.Validate() != nil {
		return classification.ErrInvalidInput
	}
	attempt.StartedAt, attempt.FinishedAt = attempt.StartedAt.UTC(), attempt.FinishedAt.UTC()
	body, err := json.Marshal(attempt)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE intent_classification_attempts SET status=status WHERE id=?`, attempt.ID); err != nil {
		return err
	}
	var id, taskID, sessionID, submissionID, status string
	var priorBody []byte
	if err = tx.QueryRowContext(ctx, `SELECT id,task_id,session_id,submission_id,status,body
		FROM intent_classification_attempts WHERE id=?`, attempt.ID).
		Scan(&id, &taskID, &sessionID, &submissionID, &status, &priorBody); err != nil {
		return err
	}
	prior, err := decodeIntentClassificationAttempt(priorBody, id, taskID, sessionID, submissionID, status)
	if err != nil {
		return err
	}
	if prior.Status != classification.AttemptStarted {
		if !bytes.Equal(priorBody, body) {
			return ErrConflict
		}
		return tx.Commit()
	}
	if prior.Version != attempt.Version || prior.ID != attempt.ID || prior.TaskID != attempt.TaskID ||
		prior.SessionID != attempt.SessionID || prior.SubmissionID != attempt.SubmissionID ||
		prior.RequestDigest != attempt.RequestDigest || prior.ConfigID != attempt.ConfigID ||
		prior.Model != attempt.Model || prior.Provider != attempt.Provider ||
		prior.EstimatedCost != attempt.EstimatedCost || !prior.StartedAt.Equal(attempt.StartedAt) {
		return ErrConflict
	}
	result, err := tx.ExecContext(ctx, `UPDATE intent_classification_attempts SET status=?,body=?
		WHERE id=? AND status=? AND body=?`, attempt.Status, body, attempt.ID, classification.AttemptStarted, priorBody)
	if err != nil {
		return err
	}
	if affected, rowsErr := result.RowsAffected(); rowsErr != nil || affected != 1 {
		return ErrConflict
	}
	return tx.Commit()
}

func (s *Store) IntentClassificationAttempt(ctx context.Context, id string) (classification.Attempt, error) {
	var storedID, taskID, sessionID, submissionID, status string
	var body []byte
	if err := s.db.QueryRowContext(ctx, `SELECT id,task_id,session_id,submission_id,status,body
		FROM intent_classification_attempts WHERE id=?`, id).
		Scan(&storedID, &taskID, &sessionID, &submissionID, &status, &body); err != nil {
		return classification.Attempt{}, err
	}
	return decodeIntentClassificationAttempt(body, storedID, taskID, sessionID, submissionID, status)
}

// IntentClassificationAttemptForSubmission returns the sole durable attempt
// admitted for submissionID. The partial unique index is the restart fence;
// direct, pre-admission attempts with an empty submission ID are not eligible.
func (s *Store) IntentClassificationAttemptForSubmission(ctx context.Context, submissionID string) (classification.Attempt, error) {
	if !intentClassificationIdentifier(submissionID) {
		return classification.Attempt{}, classification.ErrInvalidInput
	}
	var id, taskID, sessionID, storedSubmissionID, status string
	var body []byte
	if err := s.db.QueryRowContext(ctx, `SELECT id,task_id,session_id,submission_id,status,body
		FROM intent_classification_attempts WHERE submission_id=?`, submissionID).
		Scan(&id, &taskID, &sessionID, &storedSubmissionID, &status, &body); err != nil {
		return classification.Attempt{}, err
	}
	return decodeIntentClassificationAttempt(body, id, taskID, sessionID, storedSubmissionID, status)
}

func intentClassificationIdentifier(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for index, character := range []byte(value) {
		if character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z' ||
			character >= '0' && character <= '9' || index > 0 && (character == '_' || character == '.' || character == '-') {
			continue
		}
		return false
	}
	return true
}

func decodeIntentClassificationAttempt(body []byte, id, taskID, sessionID, submissionID, status string) (classification.Attempt, error) {
	var attempt classification.Attempt
	if json.Unmarshal(body, &attempt) != nil || attempt.Validate() != nil ||
		attempt.ID != id || attempt.TaskID != taskID || attempt.SessionID != sessionID ||
		attempt.SubmissionID != submissionID || attempt.Status != status {
		return classification.Attempt{}, classification.ErrInvalidInput
	}
	return attempt, nil
}

func intentClassificationAttemptForEvent(ctx context.Context, tx *sql.Tx, event runtime.Event) (classification.Attempt, error) {
	use := event.Data.IntentClassification
	if event.Kind != runtime.TaskStarted || use == nil || use.Validate() != nil {
		return classification.Attempt{}, classification.ErrInvalidInput
	}
	var id, taskID, sessionID, submissionID, status string
	var body []byte
	if err := tx.QueryRowContext(ctx, `SELECT id,task_id,session_id,submission_id,status,body
		FROM intent_classification_attempts WHERE id=?`, use.AttemptID).
		Scan(&id, &taskID, &sessionID, &submissionID, &status, &body); err != nil {
		return classification.Attempt{}, err
	}
	attempt, err := decodeIntentClassificationAttempt(body, id, taskID, sessionID, submissionID, status)
	if err != nil || !intentClassificationMatchesEvent(attempt, event) {
		return classification.Attempt{}, classification.ErrInvalidInput
	}
	return attempt, nil
}

func intentClassificationMatchesEvent(attempt classification.Attempt, event runtime.Event) bool {
	use := event.Data.IntentClassification
	if use == nil || attempt.TaskID != event.TaskID || attempt.SessionID != event.SessionID ||
		attempt.SubmissionID != event.Data.SubmissionID || attempt.ConfigID != event.Data.ConfigID ||
		attempt.ID != use.AttemptID || attempt.Status != use.Status || attempt.Code != use.Code {
		return false
	}
	if attempt.Status != classification.AttemptCompleted {
		return use.DecisionDigest == ""
	}
	if attempt.Decision == nil {
		return false
	}
	body, err := json.Marshal(attempt.Decision)
	if err != nil {
		return false
	}
	digest := sha256.Sum256(body)
	return use.DecisionDigest == hex.EncodeToString(digest[:])
}
