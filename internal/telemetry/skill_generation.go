package telemetry

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/skills"
)

var ErrSkillGenerationUnavailable = errors.New("skill generation storage unavailable")

const maxSkillGenerationBody = 512 << 10

var _ skills.GenerationRecorder = (*Store)(nil)

// Columns are bounded before SQLite materializes untrusted payloads for Go.
// A malformed row is rejected, not silently hidden by a length WHERE clause.
const skillGenerationColumns = `CASE WHEN length(CAST(id AS BLOB))<=64 THEN id END,
CASE WHEN length(CAST(scope AS BLOB))<=64 THEN scope END,
CASE WHEN length(CAST(name AS BLOB))<=64 THEN name END,
CASE WHEN length(CAST(status AS BLOB))<=16 THEN status END,
length(CAST(body AS BLOB)),CASE WHEN length(CAST(body AS BLOB))<=524288 THEN body END`

func skillGenerationID(id string) bool {
	if len(id) < 1 || len(id) > 64 {
		return false
	}
	for i, c := range []byte(id) {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			continue
		}
		if i > 0 && (c == '_' || c == '-') {
			continue
		}
		return false
	}
	return true
}

func skillGenerationBody(a skills.GenerationAttempt) ([]byte, error) {
	if a.Validate() != nil {
		return nil, skills.ErrInvalid
	}
	a.StartedAt = a.StartedAt.UTC()
	if !a.FinishedAt.IsZero() {
		a.FinishedAt = a.FinishedAt.UTC()
	}
	body, err := json.Marshal(a)
	if err != nil || len(body) > maxSkillGenerationBody {
		return nil, skills.ErrInvalid
	}
	return body, nil
}

func skillGenerationStorageError(ctx context.Context, err error) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, sql.ErrNoRows) {
		return sql.ErrNoRows
	}
	return ErrSkillGenerationUnavailable
}

type skillGenerationScanner interface{ Scan(...any) error }

func decodeSkillGeneration(row skillGenerationScanner) (skills.GenerationAttempt, error) {
	var id, scope, name, status sql.NullString
	var size sql.NullInt64
	var body []byte
	if err := row.Scan(&id, &scope, &name, &status, &size, &body); err != nil {
		return skills.GenerationAttempt{}, err
	}
	if !id.Valid || !scope.Valid || !name.Valid || !status.Valid || !size.Valid || size.Int64 < 1 || size.Int64 > maxSkillGenerationBody || int64(len(body)) != size.Int64 {
		return skills.GenerationAttempt{}, skills.ErrInvalid
	}
	var a skills.GenerationAttempt
	if json.Unmarshal(body, &a) != nil || a.Validate() != nil || a.ID != id.String || a.Key.Scope != scope.String || a.Key.Name != name.String || a.Status != status.String {
		return skills.GenerationAttempt{}, skills.ErrInvalid
	}
	return a, nil
}

func (s *Store) skillGenerationTx(ctx context.Context, id string) (*sql.Tx, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, skillGenerationStorageError(ctx, err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE skill_generation_attempts SET body=body WHERE id=?`, id); err != nil {
		tx.Rollback()
		return nil, skillGenerationStorageError(ctx, err)
	}
	return tx, nil
}

func readSkillGeneration(ctx context.Context, tx *sql.Tx, id string) (skills.GenerationAttempt, error) {
	a, err := decodeSkillGeneration(tx.QueryRowContext(ctx, `SELECT `+skillGenerationColumns+` FROM skill_generation_attempts WHERE id=?`, id))
	if err != nil && !errors.Is(err, skills.ErrInvalid) {
		return skills.GenerationAttempt{}, skillGenerationStorageError(ctx, err)
	}
	if err == nil && a.ID != id {
		return skills.GenerationAttempt{}, skills.ErrInvalid
	}
	return a, err
}

// BeginSkillGeneration is a one-winner execution claim, not an idempotent
// permission to run. Every existing ID conflicts, including an identical start.
// Provenance is a trusted host snapshot; this primitive does not inspect tasks.
func (s *Store) BeginSkillGeneration(ctx context.Context, a skills.GenerationAttempt) error {
	return s.beginSkillGeneration(ctx, a, nil)
}

func (s *Store) beginSkillGeneration(ctx context.Context, a skills.GenerationAttempt, budget *skills.GenerationBudget) error {
	if ctx == nil || s == nil || s.db == nil || a.Status != "started" {
		return skills.ErrInvalid
	}
	body, err := skillGenerationBody(a)
	if err != nil {
		return err
	}
	tx, err := s.skillGenerationTx(ctx, a.ID)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = readSkillGeneration(ctx, tx, a.ID)
	if err == nil {
		return ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if budget != nil {
		history, err := skillGenerationBudgetHistory(ctx, tx, a.Key.Scope)
		if err != nil {
			return err
		}
		if err = budget.Check(a, history, time.Now().UTC()); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO skill_generation_attempts(id,scope,name,status,body) VALUES(?,?,?,?,?)`, a.ID, a.Key.Scope, a.Key.Name, a.Status, body); err != nil {
		return skillGenerationStorageError(ctx, err)
	}
	if err = tx.Commit(); err != nil {
		return skillGenerationStorageError(ctx, err)
	}
	return nil
}

// FinishSkillGeneration atomically records a terminal proposal/failure. Exact
// terminal retries are safe; immutable input changes and overwrites conflict.
func (s *Store) FinishSkillGeneration(ctx context.Context, a skills.GenerationAttempt) error {
	if ctx == nil || s == nil || s.db == nil || (a.Status != "failed" && a.Status != "drafted") {
		return skills.ErrInvalid
	}
	body, err := skillGenerationBody(a)
	if err != nil {
		return err
	}
	tx, err := s.skillGenerationTx(ctx, a.ID)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	prior, err := readSkillGeneration(ctx, tx, a.ID)
	if err != nil {
		return err
	}
	priorBody, err := skillGenerationBody(prior)
	if err != nil {
		return err
	}
	if prior.Status != "started" {
		if !bytes.Equal(body, priorBody) {
			return ErrConflict
		}
		if err = tx.Commit(); err != nil {
			return skillGenerationStorageError(ctx, err)
		}
		return nil
	}
	initial := a
	initial.Status = "started"
	initial.Code = ""
	initial.FinishedAt = time.Time{}
	initial.Result = nil
	initialBody, err := skillGenerationBody(initial)
	if err != nil {
		return err
	}
	if !bytes.Equal(initialBody, priorBody) {
		return ErrConflict
	}
	result, err := tx.ExecContext(ctx, `UPDATE skill_generation_attempts SET status=?,body=? WHERE id=? AND status='started'`, a.Status, body, a.ID)
	if err != nil {
		return skillGenerationStorageError(ctx, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return skillGenerationStorageError(ctx, err)
	}
	if n != 1 {
		return ErrConflict
	}
	if err = tx.Commit(); err != nil {
		return skillGenerationStorageError(ctx, err)
	}
	return nil
}

func (s *Store) SkillGenerationAttempt(ctx context.Context, id string) (skills.GenerationAttempt, error) {
	if ctx == nil || s == nil || s.db == nil || !skillGenerationID(id) {
		return skills.GenerationAttempt{}, skills.ErrInvalid
	}
	a, err := decodeSkillGeneration(s.db.QueryRowContext(ctx, `SELECT `+skillGenerationColumns+` FROM skill_generation_attempts WHERE id=?`, id))
	if err != nil {
		if errors.Is(err, skills.ErrInvalid) {
			return skills.GenerationAttempt{}, err
		}
		return skills.GenerationAttempt{}, skillGenerationStorageError(ctx, err)
	}
	if a.ID != id {
		return skills.GenerationAttempt{}, skills.ErrInvalid
	}
	return a, nil
}

// ListSkillGenerationAttempts is a bounded scope-local lexical scan. Separate
// pages are live observations, not a frozen snapshot; no work is dispatched.
func (s *Store) ListSkillGenerationAttempts(ctx context.Context, scope, after string, limit int) ([]skills.GenerationAttempt, error) {
	if ctx == nil || s == nil || s.db == nil || !skillGenerationID(scope) || (after != "" && !skillGenerationID(after)) || limit < 1 || limit > 100 {
		return nil, skills.ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+skillGenerationColumns+` FROM skill_generation_attempts WHERE scope=? AND id>? ORDER BY id LIMIT ?`, scope, after, limit)
	if err != nil {
		return nil, skillGenerationStorageError(ctx, err)
	}
	defer rows.Close()
	result := make([]skills.GenerationAttempt, 0)
	previous := after
	for rows.Next() {
		a, err := decodeSkillGeneration(rows)
		if err != nil {
			if errors.Is(err, skills.ErrInvalid) {
				return nil, err
			}
			return nil, skillGenerationStorageError(ctx, err)
		}
		if a.Key.Scope != scope || a.ID <= previous {
			return nil, skills.ErrInvalid
		}
		result = append(result, a)
		previous = a.ID
	}
	if err = rows.Err(); err != nil {
		return nil, skillGenerationStorageError(ctx, err)
	}
	return result, nil
}
