package telemetry

import (
	"context"
	"database/sql"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/traces"
)

// readSkillGenerationTraces projects terminal generation attempts as
// independent operations. They are not attached to source tasks because one
// draft may use several already-terminal tasks and finish after all of them.
func readSkillGenerationTraces(ctx context.Context, tx *sql.Tx, limit int, observedAt time.Time) ([]traces.Trace, error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+skillGenerationColumns+`
		FROM skill_generation_attempts WHERE status IN ('drafted','failed') ORDER BY rowid DESC LIMIT ?`, limit)
	if err != nil {
		return nil, errTraces
	}
	defer rows.Close()
	out := make([]traces.Trace, 0)
	for rows.Next() {
		attempt, decodeErr := decodeSkillGeneration(rows)
		if decodeErr != nil || attempt.Status != "drafted" && attempt.Status != "failed" || attempt.StartedAt.After(attempt.FinishedAt) || attempt.FinishedAt.After(observedAt) {
			return nil, errTraces
		}
		out = append(out, traces.Trace{Spans: []traces.Span{{
			Name: "skill_generation", Outcome: attempt.Status, Parent: -1,
			StartedAt: attempt.StartedAt, EndedAt: attempt.FinishedAt,
		}}})
	}
	if rows.Err() != nil || rows.Close() != nil {
		return nil, errTraces
	}
	return out, nil
}
