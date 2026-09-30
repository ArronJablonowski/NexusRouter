package telemetry

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/ArronJablonowski/NexusRouter/skills"
)

var ErrLearningUnavailable = errors.New("learning state storage unavailable")

func learningError(ctx context.Context, err error) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, ErrConflict) || errors.Is(err, skills.ErrInvalid) {
		return err
	}
	return ErrLearningUnavailable
}

const learningColumns = `CASE WHEN length(CAST(scope AS BLOB))<=64 THEN scope END, CASE WHEN length(CAST(name AS BLOB))<=64 THEN name END, revision, length(CAST(body AS BLOB)), CASE WHEN length(CAST(body AS BLOB))<=4096 THEN body END`

func decodeLearning(row skillGenerationScanner) (skills.LearningState, error) {
	var scope, name sql.NullString
	var revision, size sql.NullInt64
	var body []byte
	var state skills.LearningState
	if err := row.Scan(&scope, &name, &revision, &size, &body); err != nil {
		return state, err
	}
	if !scope.Valid || !name.Valid || !revision.Valid || !size.Valid || size.Int64 < 1 || size.Int64 > 4096 || int64(len(body)) != size.Int64 || json.Unmarshal(body, &state) != nil || state.Validate() != nil || state.Scope != scope.String || state.Name != name.String || state.Revision != revision.Int64 {
		return skills.LearningState{}, skills.ErrInvalid
	}
	canonical, err := json.Marshal(state)
	if err != nil || !bytes.Equal(canonical, body) {
		return skills.LearningState{}, skills.ErrInvalid
	}
	return state, nil
}

// LearningState inspects one bounded durable cursor without mutating it.
func (s *Store) LearningState(ctx context.Context, scope, name string) (skills.LearningState, error) {
	if ctx == nil || s == nil || s.db == nil || !skillGenerationID(scope) || !skillGenerationID(name) {
		return skills.LearningState{}, skills.ErrInvalid
	}
	state, err := decodeLearning(s.db.QueryRowContext(ctx, `SELECT `+learningColumns+` FROM learning_states WHERE scope=? AND name=?`, scope, name))
	if err != nil {
		return skills.LearningState{}, learningError(ctx, err)
	}
	return state, nil
}

// PutLearningState performs exact revision CAS. No callback or external work is
// performed in this transaction; the generation ledger owns dispatch claims.
func (s *Store) PutLearningState(ctx context.Context, state skills.LearningState, expectedRevision int64) error {
	if ctx == nil || s == nil || s.db == nil || state.Validate() != nil || expectedRevision < 0 || state.Revision != expectedRevision+1 {
		return skills.ErrInvalid
	}
	body, _ := json.Marshal(state)
	if len(body) > 4096 {
		return skills.ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return learningError(ctx, err)
	}
	defer tx.Rollback()
	// Acquire SQLite's writer reservation before reading the CAS baseline.
	if _, err = tx.ExecContext(ctx, `UPDATE learning_states SET revision=revision WHERE scope=? AND name=?`, state.Scope, state.Name); err != nil {
		return learningError(ctx, err)
	}
	previous, err := decodeLearning(tx.QueryRowContext(ctx, `SELECT `+learningColumns+` FROM learning_states WHERE scope=? AND name=?`, state.Scope, state.Name))
	if errors.Is(err, sql.ErrNoRows) {
		if expectedRevision != 0 {
			return ErrConflict
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO learning_states(scope,name,revision,body) VALUES(?,?,?,?)`, state.Scope, state.Name, state.Revision, body)
	} else {
		if err != nil {
			return learningError(ctx, err)
		}
		if previous.Revision != expectedRevision {
			return ErrConflict
		}
		if state.ValidateAfter(previous) != nil {
			return skills.ErrInvalid
		}
		var result sql.Result
		result, err = tx.ExecContext(ctx, `UPDATE learning_states SET revision=?,body=? WHERE scope=? AND name=? AND revision=?`, state.Revision, body, state.Scope, state.Name, expectedRevision)
		if err == nil {
			var n int64
			n, err = result.RowsAffected()
			if err == nil && n != 1 {
				return ErrConflict
			}
		}
	}
	if err != nil {
		return learningError(ctx, err)
	}
	if err = tx.Commit(); err != nil {
		return learningError(ctx, err)
	}
	return nil
}
