package telemetry

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/skills"
)

func activationIntentSelection(id string) bool {
	b, err := hex.DecodeString(id)
	return len(id) == 64 && err == nil && hex.EncodeToString(b) == id
}

func decodeActivationIntent(row skillGenerationScanner, scope, name, selection string) (skills.LearningActivationIntent, error) {
	var body []byte
	var out skills.LearningActivationIntent
	if err := row.Scan(&body); err != nil {
		return out, err
	}
	if len(body) < 1 || len(body) > 4096 || json.Unmarshal(body, &out) != nil || out.Validate() != nil || out.Scope != scope || out.Name != name || out.SelectionID != selection {
		return skills.LearningActivationIntent{}, skills.ErrInvalid
	}
	canonical, err := json.Marshal(out)
	if err != nil || !bytes.Equal(body, canonical) {
		return skills.LearningActivationIntent{}, skills.ErrInvalid
	}
	return out, nil
}

const activationIntentBody = `CASE WHEN length(CAST(body AS BLOB)) BETWEEN 1 AND 4096 THEN body END`

// LearningActivationIntent reads an immutable bounded receipt without migration
// or rechecking the learning cursor, which may since have advanced.
func (s *Store) LearningActivationIntent(ctx context.Context, scope, name, selectionID string) (skills.LearningActivationIntent, error) {
	if s == nil || s.db == nil || ctx == nil || !skillGenerationID(scope) || !skillGenerationID(name) || !activationIntentSelection(selectionID) {
		return skills.LearningActivationIntent{}, skills.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return skills.LearningActivationIntent{}, learningError(ctx, err)
	}
	defer tx.Rollback()
	var schema int
	if err := tx.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&schema); err != nil {
		return skills.LearningActivationIntent{}, learningError(ctx, err)
	}
	if schema < 26 || schema > 30 {
		return skills.LearningActivationIntent{}, ErrLearningUnavailable
	}
	out, err := decodeActivationIntent(tx.QueryRowContext(ctx, `SELECT `+activationIntentBody+` FROM learning_activation_intents WHERE scope=? AND name=? AND selection_id=?`, scope, name, selectionID), scope, name, selectionID)
	if err != nil {
		return skills.LearningActivationIntent{}, learningError(ctx, err)
	}
	if err := tx.Commit(); err != nil {
		return skills.LearningActivationIntent{}, learningError(ctx, err)
	}
	return out, nil
}

// PutLearningActivationIntent pins one candidate/precondition for the active
// generation cursor. Exact repeats are allowed; no intent or learning row changes.
func (s *Store) PutLearningActivationIntent(ctx context.Context, intent skills.LearningActivationIntent) error {
	if s == nil || s.db == nil || ctx == nil || intent.Validate() != nil {
		return skills.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	body, err := json.Marshal(intent)
	if err != nil || len(body) > 4096 {
		return skills.ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return learningError(ctx, err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE learning_activation_intents SET body=body WHERE 0`); err != nil {
		return learningError(ctx, err)
	}
	var schema int
	if err = tx.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&schema); err != nil {
		return learningError(ctx, err)
	}
	if schema < 26 || schema > 30 {
		return ErrLearningUnavailable
	}
	state, err := decodeLearning(tx.QueryRowContext(ctx, `SELECT `+learningColumns+` FROM learning_states WHERE scope=? AND name=?`, intent.Scope, intent.Name))
	if err != nil {
		return learningError(ctx, err)
	}
	if state.Scope != intent.Scope || state.Name != intent.Name || state.Revision != intent.LearningRevision || state.PolicyDigest != intent.PolicyDigest || state.Phase != "generate" || state.PendingSelectionID != intent.SelectionID || state.PendingBucketID != intent.Expected.Key.Name {
		return ErrConflict
	}
	attempt, err := readSkillGeneration(ctx, tx, intent.SelectionID)
	if err != nil {
		return learningError(ctx, err)
	}
	if attempt.ID != intent.SelectionID || attempt.Status != "drafted" || attempt.Key != intent.Expected.Key {
		return ErrConflict
	}
	previous, err := decodeActivationIntent(tx.QueryRowContext(ctx, `SELECT `+activationIntentBody+` FROM learning_activation_intents WHERE scope=? AND name=? AND selection_id=?`, intent.Scope, intent.Name, intent.SelectionID), intent.Scope, intent.Name, intent.SelectionID)
	if err == nil {
		old, _ := json.Marshal(previous)
		if !bytes.Equal(old, body) {
			return ErrConflict
		}
	} else if errors.Is(err, sql.ErrNoRows) {
		result, insertErr := tx.ExecContext(ctx, `INSERT INTO learning_activation_intents(scope,name,selection_id,body) VALUES(?,?,?,?)`, intent.Scope, intent.Name, intent.SelectionID, body)
		if insertErr != nil {
			return learningError(ctx, insertErr)
		}
		if n, err := result.RowsAffected(); err != nil || n != 1 {
			return ErrConflict
		}
	} else {
		return learningError(ctx, err)
	}
	if err = tx.Commit(); err != nil {
		return learningError(ctx, err)
	}
	return nil
}
