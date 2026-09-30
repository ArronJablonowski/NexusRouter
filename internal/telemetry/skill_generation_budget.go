package telemetry

import (
	"context"
	"database/sql"
	"time"

	"github.com/ArronJablonowski/NexusRouter/skills"
)

// BeginSkillGenerationBudgeted reserves estimated cost and attempt capacity
// under the same writer lock as the one-use dispatch claim. This is not actual
// provider billing. Every scope attempt counts, including unbudgeted history;
// an unresolved started attempt is never presumed safe to refund or redispatch.
func (s *Store) BeginSkillGenerationBudgeted(ctx context.Context, a skills.GenerationAttempt, budget skills.GenerationBudget) error {
	if ctx == nil || budget.Validate() != nil {
		return skills.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return s.beginSkillGeneration(ctx, a, &budget)
}

func skillGenerationBudgetHistory(ctx context.Context, tx *sql.Tx, scope string) ([]skills.GenerationAttempt, error) {
	// Bound the complete scan before any body is materialized in Go. A corrupt
	// or overfull scope fails closed; records are not hidden by WHERE lengths.
	rows, err := tx.QueryContext(ctx, `SELECT CASE WHEN length(CAST(id AS BLOB)) BETWEEN 1 AND 64 THEN id END,length(CAST(body AS BLOB)) FROM skill_generation_attempts WHERE scope=? ORDER BY id LIMIT 1001`, scope)
	if err != nil {
		return nil, skillGenerationStorageError(ctx, err)
	}
	ids := []string{}
	total := int64(0)
	previous := ""
	for rows.Next() {
		var id sql.NullString
		var size sql.NullInt64
		if err = rows.Scan(&id, &size); err != nil {
			rows.Close()
			return nil, skillGenerationStorageError(ctx, err)
		}
		if !id.Valid || !skillGenerationID(id.String) || id.String <= previous || !size.Valid || size.Int64 < 1 || size.Int64 > maxSkillGenerationBody || size.Int64 > (8<<20)-total || len(ids) >= 1000 {
			rows.Close()
			return nil, skills.ErrInvalid
		}
		total += size.Int64
		ids = append(ids, id.String)
		previous = id.String
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, skillGenerationStorageError(ctx, err)
	}
	history := make([]skills.GenerationAttempt, 0, len(ids))
	for _, id := range ids {
		a, err := readSkillGeneration(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if a.Key.Scope != scope {
			return nil, skills.ErrInvalid
		}
		history = append(history, a)
	}
	return history, nil
}
