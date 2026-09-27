package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/routing"
)

// RecordEvaluation atomically commits immutable evidence and aggregate fitness.
// One accepted record per model attempt prevents repeated feedback from inflating
// sample counts. SupersedeEvaluation provides explicit subjective revisions.
func (s *Store) RecordEvaluation(ctx context.Context, r evaluation.Record) error {
	if err := r.Validate(); err != nil {
		return err
	}
	r.Time = r.Time.UTC()
	body, err := json.Marshal(r)
	if err != nil {
		return err
	}
	out, err := evaluation.Resolve(r.Checks, r.AllowJudge)
	if err != nil {
		return err
	}
	if out.Source == evaluation.Withdrawn {
		return evaluation.ErrEvidence // withdrawal requires an existing subjective head
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "UPDATE task_heads SET sequence=sequence WHERE task_id=?", r.TaskID); err != nil {
		return err
	}
	var prior []byte
	err = tx.QueryRowContext(ctx, "SELECT body FROM evaluations WHERE id=?", r.ID).Scan(&prior)
	if err == nil {
		if string(prior) != string(body) {
			return ErrConflict
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var reused int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM evaluation_revisions WHERE id=?", r.ID).Scan(&reused); err != nil {
		return err
	}
	if reused != 0 {
		return ErrConflict
	}
	var attempts int
	// Verify attribution to a persisted model attempt, not an arbitrary caller key.
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM events WHERE task_id=? AND json_extract(body,'$.attempt_id')=? AND json_extract(body,'$.kind')='turn.started' AND json_extract(body,'$.data.model_id')=? AND json_extract(body,'$.data.provider_id')=?`, r.TaskID, r.AttemptID, r.Key.Model, r.Key.Provider).Scan(&attempts)
	if err != nil {
		return err
	}
	if attempts != 1 {
		return evaluation.ErrEvidence
	}
	var ended int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM events WHERE task_id=? AND json_extract(body,'$.attempt_id')=? AND json_extract(body,'$.kind') IN ('turn.completed','task.failed','task.canceled')`, r.TaskID, r.AttemptID).Scan(&ended); err != nil {
		return err
	}
	if ended == 0 {
		return evaluation.ErrEvidence
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO evaluations VALUES(?,?,?,?,?,?,?,?)`, r.ID, r.TaskID, r.AttemptID, r.Key.Model, r.Key.Provider, r.Key.Domain, r.Key.Profile, body); err != nil {
		return ErrConflict
	}
	quality, reliability, compliance, schemas := 0, 0, 0, 0
	if _, err = tx.ExecContext(ctx, "INSERT INTO evaluation_heads VALUES(?,?)", r.ID, r.ID); err != nil {
		return err
	}
	if out.Accepted {
		quality = 1
	}
	if r.ExecutionSucceeded {
		reliability = 1
	}
	if r.SchemaPassed != nil {
		schemas = 1
		if *r.SchemaPassed {
			compliance = 1
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO fitness VALUES(?,?,?,?,1,?,?,?,?,?,?,?)
	 ON CONFLICT(model,provider,domain,profile) DO UPDATE SET samples=samples+1,
	 quality=quality+excluded.quality, compliance=compliance+excluded.compliance,
	 schema_samples=schema_samples+excluded.schema_samples, reliability=reliability+excluded.reliability,
	 latency=latency+excluded.latency,cost=cost+excluded.cost,updated=max(updated,excluded.updated)`, r.Key.Model, r.Key.Provider, r.Key.Domain, r.Key.Profile, quality, compliance, schemas, reliability, float64(r.Latency), r.Cost, r.Time.UnixNano())
	if err != nil {
		return err
	}
	return tx.Commit()
}

// Fitness returns the legacy lifetime projection retained for compatibility and
// transactional write checks. Adaptive routing reads ObservationSet and applies
// event-time decay with an explicit clock and configuration snapshot.
func (s *Store) Fitness(ctx context.Context, key routing.Key) (routing.Evidence, error) {
	var e routing.Evidence
	var schemas int
	var latency float64
	var updated int64
	err := s.db.QueryRowContext(ctx, `SELECT samples,quality,compliance,schema_samples,reliability,latency,cost,updated FROM fitness WHERE model=? AND provider=? AND domain=? AND profile=?`, key.Model, key.Provider, key.Domain, key.Profile).Scan(&e.Samples, &e.Quality, &e.Compliance, &schemas, &e.Reliability, &latency, &e.Cost, &updated)
	if err != nil {
		return e, err
	}
	n := float64(e.Samples)
	if n == 0 {
		return routing.Evidence{}, sql.ErrNoRows
	}
	e.Quality /= n
	e.Reliability /= n
	e.Cost /= n
	e.Latency = time.Duration(latency / n)
	e.Updated = time.Unix(0, updated).UTC()
	if schemas > 0 {
		e.Compliance /= float64(schemas)
	} else {
		e.Compliance = .5
	}
	return e, nil
}
