package telemetry

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/ArronJablonowski/NexusRouter/skills"
)

var ErrWorkflowSelectionUnavailable = errors.New("workflow selection storage unavailable")

const workflowSelectionColumns = `CASE WHEN length(CAST(id AS BLOB))=64 THEN id END,
CASE WHEN length(CAST(scope AS BLOB))<=64 THEN scope END,
CASE WHEN length(CAST(name AS BLOB))<=64 THEN name END,
length(CAST(body AS BLOB)),CASE WHEN length(CAST(body AS BLOB))<=65536 THEN body END`

func workflowSelectionID(id string) bool {
	if len(id) != 64 || strings.ToLower(id) != id {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func workflowSelectionError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, sql.ErrNoRows) {
		return sql.ErrNoRows
	}
	if errors.Is(err, skills.ErrInvalid) {
		return skills.ErrInvalid
	}
	return ErrWorkflowSelectionUnavailable
}

func decodeWorkflowSelection(row skillGenerationScanner) (skills.WorkflowSelection, error) {
	var id, scope, name sql.NullString
	var size sql.NullInt64
	var body []byte
	if err := row.Scan(&id, &scope, &name, &size, &body); err != nil {
		return skills.WorkflowSelection{}, err
	}
	if !id.Valid || !scope.Valid || !name.Valid || !size.Valid || size.Int64 < 1 || size.Int64 > 65536 || int64(len(body)) != size.Int64 {
		return skills.WorkflowSelection{}, skills.ErrInvalid
	}
	var a skills.WorkflowSelection
	if json.Unmarshal(body, &a) != nil || a.Validate() != nil || a.ID != id.String || a.Key.Scope != scope.String || a.Key.Name != name.String {
		return skills.WorkflowSelection{}, skills.ErrInvalid
	}
	return a, nil
}

// SaveWorkflowSelection persists an immutable host-admitted selection. Source
// authenticity is the host's responsibility, not inferred from hashes. Exact
// identity retries return the first saved creation time, never overwrite it,
// and never authorize or dispatch generation.
func (s *Store) SaveWorkflowSelection(ctx context.Context, a skills.WorkflowSelection) (skills.WorkflowSelection, error) {
	zero := skills.WorkflowSelection{}
	if ctx == nil || s == nil || s.db == nil || a.Validate() != nil {
		return zero, skills.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	a.CreatedAt = a.CreatedAt.UTC()
	body, err := json.Marshal(a)
	if err != nil || len(body) > 65536 {
		return zero, skills.ErrInvalid
	}
	// Snapshot caller-owned slices before the transaction and return only the
	// decoded committed representation rather than mutable caller data.
	var owned skills.WorkflowSelection
	if json.Unmarshal(body, &owned) != nil {
		return zero, skills.ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, workflowSelectionError(ctx, err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE workflow_selections SET body=body WHERE id=?`, owned.ID); err != nil {
		return zero, workflowSelectionError(ctx, err)
	}
	prior, err := decodeWorkflowSelection(tx.QueryRowContext(ctx, `SELECT `+workflowSelectionColumns+` FROM workflow_selections WHERE id=?`, owned.ID))
	if err == nil {
		if prior.ID != owned.ID {
			return zero, skills.ErrInvalid
		}
		if err = tx.Commit(); err != nil {
			return zero, workflowSelectionError(ctx, err)
		}
		return prior, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return zero, workflowSelectionError(ctx, err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO workflow_selections(id,scope,name,body) VALUES(?,?,?,?)`, owned.ID, owned.Key.Scope, owned.Key.Name, body); err != nil {
		return zero, workflowSelectionError(ctx, err)
	}
	if err = tx.Commit(); err != nil {
		return zero, workflowSelectionError(ctx, err)
	}
	return owned, nil
}

// WorkflowSelection performs a scope-bound read without migrations or writes.
func (s *Store) WorkflowSelection(ctx context.Context, scope, id string) (skills.WorkflowSelection, error) {
	zero := skills.WorkflowSelection{}
	if ctx == nil || s == nil || s.db == nil || !workflowSourceID.MatchString(scope) || !workflowSelectionID(id) {
		return zero, skills.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	a, err := decodeWorkflowSelection(s.db.QueryRowContext(ctx, `SELECT `+workflowSelectionColumns+` FROM workflow_selections WHERE scope=? AND id=?`, scope, id))
	if err != nil {
		return zero, workflowSelectionError(ctx, err)
	}
	if a.ID != id || a.Key.Scope != scope {
		return zero, skills.ErrInvalid
	}
	return a, nil
}

// ListWorkflowSelections returns bounded metadata records in lexical ID order.
// Separate pages are live observations; they do not freeze source freshness.
func (s *Store) ListWorkflowSelections(ctx context.Context, scope, after string, limit int) ([]skills.WorkflowSelection, error) {
	if ctx == nil || s == nil || s.db == nil || !workflowSourceID.MatchString(scope) || (after != "" && !workflowSelectionID(after)) || limit < 1 || limit > 100 {
		return nil, skills.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT `+workflowSelectionColumns+` FROM workflow_selections WHERE scope=? AND id>? ORDER BY id LIMIT ?`, scope, after, limit)
	if err != nil {
		return nil, workflowSelectionError(ctx, err)
	}
	defer rows.Close()
	result := make([]skills.WorkflowSelection, 0)
	previous := after
	for rows.Next() {
		a, err := decodeWorkflowSelection(rows)
		if err != nil {
			return nil, workflowSelectionError(ctx, err)
		}
		if a.Key.Scope != scope || a.ID <= previous {
			return nil, skills.ErrInvalid
		}
		result = append(result, a)
		previous = a.ID
	}
	if err = rows.Err(); err != nil {
		return nil, workflowSelectionError(ctx, err)
	}
	return result, nil
}
