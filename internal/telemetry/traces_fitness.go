package telemetry

import (
	"context"
	"database/sql"
	"math"
	"time"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/sessions"
	"github.com/ArronJablonowski/NexusRouter/traces"
)

const maxTraceEvaluations = 100
const maxTraceEvaluationBody = 256 << 10

// readTaskFitnessTrace reports only whether a canonical base fitness mutation
// and any later subjective revision exist. It validates their bounded history
// and aggregate projection but exports no model, provider, domain, profile,
// score, evidence, evaluator, revision, task or attempt identity.
func readTaskFitnessTrace(ctx context.Context, tx *sql.Tx, task string, at, observedAt time.Time) ([]traces.Span, error) {
	rows, err := tx.QueryContext(ctx, `SELECT
	CASE WHEN typeof(id)='text' AND length(CAST(id AS BLOB)) BETWEEN 1 AND 128 THEN id END,
	CASE WHEN typeof(attempt_id)='text' AND length(CAST(attempt_id AS BLOB)) BETWEEN 1 AND 128 THEN attempt_id END,
	CASE WHEN typeof(body)='blob' AND length(body) BETWEEN 1 AND ? THEN 1 ELSE 0 END
	FROM evaluations WHERE task_id=? ORDER BY rowid LIMIT ?`, maxTraceEvaluationBody, task, maxTraceEvaluations+1)
	if err != nil {
		return nil, errTraces
	}
	type pair struct{ id, attempt string }
	pairs := make([]pair, 0)
	for rows.Next() {
		var id, attempt sql.NullString
		var bounded int
		if rows.Scan(&id, &attempt, &bounded) != nil || len(pairs) >= maxTraceEvaluations || !id.Valid || !attempt.Valid ||
			!sessions.ValidEventPageID(id.String) || !sessions.ValidEventPageID(attempt.String) || bounded != 1 {
			rows.Close()
			return nil, errTraces
		}
		pairs = append(pairs, pair{id: id.String, attempt: attempt.String})
	}
	if rows.Err() != nil || rows.Close() != nil {
		return nil, errTraces
	}
	revised := false
	for _, item := range pairs {
		var revisionCount, invalidRevisions int
		if tx.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(CASE WHEN
			typeof(id)='text' AND length(CAST(id AS BLOB)) BETWEEN 1 AND 128 AND
			typeof(supersedes)='text' AND length(CAST(supersedes AS BLOB)) BETWEEN 1 AND 128 AND
			typeof(body)='blob' AND length(body) BETWEEN 1 AND ? THEN 0 ELSE 1 END),0)
			FROM evaluation_revisions WHERE base_id=?`, maxTraceEvaluationBody, item.id).Scan(&revisionCount, &invalidRevisions) != nil ||
			revisionCount < 0 || revisionCount > 100 || invalidRevisions != 0 {
			return nil, errTraces
		}
		history, historyErr := evaluationHistory(ctx, tx, task, item.attempt)
		if historyErr != nil || len(history) != revisionCount+1 || history[0].ID != item.id {
			return nil, errTraces
		}
		for _, record := range history {
			if record.Time.Before(time.Unix(0, 0).UTC()) || record.Time.After(observedAt) {
				return nil, errTraces
			}
		}
		if validateTraceFitnessProjection(ctx, tx, history[len(history)-1]) != nil {
			return nil, errTraces
		}
		revised = revised || revisionCount > 0
	}
	if len(pairs) == 0 {
		return []traces.Span{}, nil
	}
	out := []traces.Span{traceInstant("fitness_update", "recorded", at)}
	if revised {
		out = append(out, traceInstant("fitness_update", "revised", at))
	}
	return out, nil
}

func validateTraceFitnessProjection(ctx context.Context, tx *sql.Tx, record evaluation.Record) error {
	minimumSamples := int64(1)
	if outcome, err := evaluation.Resolve(record.Checks, record.AllowJudge); err == nil && outcome.Source == evaluation.Withdrawn {
		minimumSamples = 0
	}
	var samples, schemaSamples sql.NullInt64
	var quality, compliance, reliability, latency, cost sql.NullFloat64
	var updated sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT
	CASE WHEN typeof(samples)='integer' THEN samples END,
	CASE WHEN typeof(quality) IN ('integer','real') THEN quality END,
	CASE WHEN typeof(compliance) IN ('integer','real') THEN compliance END,
	CASE WHEN typeof(schema_samples)='integer' THEN schema_samples END,
	CASE WHEN typeof(reliability) IN ('integer','real') THEN reliability END,
	CASE WHEN typeof(latency) IN ('integer','real') THEN latency END,
	CASE WHEN typeof(cost) IN ('integer','real') THEN cost END,
	CASE WHEN typeof(updated)='integer' THEN updated END
	FROM fitness WHERE model=? AND provider=? AND domain=? AND profile=?`, record.Key.Model, record.Key.Provider, record.Key.Domain, record.Key.Profile).
		Scan(&samples, &quality, &compliance, &schemaSamples, &reliability, &latency, &cost, &updated)
	if err != nil || !samples.Valid || !schemaSamples.Valid || !quality.Valid || !compliance.Valid || !reliability.Valid || !latency.Valid || !cost.Valid || !updated.Valid ||
		samples.Int64 < minimumSamples || schemaSamples.Int64 < 0 || schemaSamples.Int64 > samples.Int64 ||
		invalidFitnessFloat(quality.Float64, 0, float64(samples.Int64)) ||
		invalidFitnessFloat(compliance.Float64, 0, float64(schemaSamples.Int64)) ||
		invalidFitnessFloat(reliability.Float64, 0, float64(samples.Int64)) ||
		invalidFitnessFloat(latency.Float64, 0, math.MaxFloat64) || invalidFitnessFloat(cost.Float64, 0, math.MaxFloat64) ||
		updated.Int64 < 0 || time.Unix(0, updated.Int64).UTC().Year() >= 2261 {
		return errTraces
	}
	return nil
}

func invalidFitnessFloat(value, minimum, maximum float64) bool {
	return math.IsNaN(value) || math.IsInf(value, 0) || value < minimum || value > maximum
}
