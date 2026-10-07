package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
)

// SupersedeEvaluation replaces only a subjective quality contribution. Original
// evidence and all revisions remain immutable; fitness retains one sample.
func (s *Store) SupersedeEvaluation(ctx context.Context, expectedID string, r evaluation.Record) error {
	return s.supersedeEvaluation(ctx, expectedID, r, evaluation.ValidateRevision)
}

// CorrectObjectiveAttribution is only for trusted operator/evaluator adapters,
// never the chat feedback endpoint. Original grades remain in history and the
// current verdict, scope and measurements cannot change.
func (s *Store) CorrectObjectiveAttribution(ctx context.Context, expectedID string, r evaluation.Record) error {
	return s.supersedeEvaluation(ctx, expectedID, r, evaluation.ValidateObjectiveCorrection)
}

func (s *Store) supersedeEvaluation(ctx context.Context, expectedID string, r evaluation.Record, validate func(evaluation.Record, evaluation.Record) error) error {
	if expectedID == "" || r.Validate() != nil {
		return evaluation.ErrEvidence
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
	if _, err = tx.ExecContext(ctx, "UPDATE task_heads SET sequence=sequence WHERE task_id=?", r.TaskID); err != nil {
		return err
	}
	var priorBody []byte
	var supersedes string
	err = tx.QueryRowContext(ctx, "SELECT supersedes,body FROM evaluation_revisions WHERE id=?", r.ID).Scan(&supersedes, &priorBody)
	if err == nil {
		if supersedes != expectedID || string(priorBody) != string(body) {
			return ErrConflict
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var existing int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM evaluations WHERE id=?", r.ID).Scan(&existing); err != nil {
		return err
	}
	if existing != 0 {
		return ErrConflict
	}
	history, err := evaluationHistory(ctx, tx, r.TaskID, r.AttemptID)
	if err != nil {
		return err
	}
	prior := history[len(history)-1]
	if prior.ID != expectedID {
		return ErrConflict
	}
	if len(history) > 100 {
		return evaluation.ErrEvidence
	}
	if err = validate(prior, r); err != nil {
		return err
	}
	// Corrections must also match the original grade so readers which project
	// base-to-head measurements cannot mistake a changed verdict for attribution.
	outcome, _ := evaluation.Resolve(r.Checks, r.AllowJudge)
	if outcome.Source == evaluation.Deterministic && evaluation.ValidateObjectiveCorrection(history[0], r) != nil {
		return evaluation.ErrEvidence
	}
	oldOutcome, _ := evaluation.Resolve(prior.Checks, prior.AllowJudge)
	newOutcome, _ := evaluation.Resolve(r.Checks, r.AllowJudge)
	delta := 0
	if oldOutcome.Accepted && oldOutcome.Source != evaluation.Withdrawn {
		delta--
	}
	if newOutcome.Accepted && newOutcome.Source != evaluation.Withdrawn {
		delta++
	}
	sampleDelta := 0
	if oldOutcome.Source == evaluation.Withdrawn {
		sampleDelta++
	}
	if newOutcome.Source == evaluation.Withdrawn {
		sampleDelta--
	}
	reliability, schemas, compliance := 0, 0, 0
	if r.ExecutionSucceeded {
		reliability = sampleDelta
	}
	if r.SchemaPassed != nil {
		schemas = sampleDelta
		if *r.SchemaPassed {
			compliance = sampleDelta
		}
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO evaluation_revisions(id,base_id,supersedes,body) VALUES(?,?,?,?)", r.ID, history[0].ID, expectedID, body); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "UPDATE evaluation_heads SET current_id=? WHERE base_id=? AND current_id=?", r.ID, history[0].ID, expectedID)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return ErrConflict
	}
	result, err = tx.ExecContext(ctx, "UPDATE fitness SET quality=quality+?,samples=samples+?,reliability=reliability+?,schema_samples=schema_samples+?,compliance=compliance+?,latency=latency+?,cost=cost+? WHERE model=? AND provider=? AND domain=? AND profile=?", delta, sampleDelta, reliability, schemas, compliance, float64(sampleDelta)*float64(r.Latency), float64(sampleDelta)*r.Cost, r.Key.Model, r.Key.Provider, r.Key.Domain, r.Key.Profile)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return evaluation.ErrEvidence
	}
	return tx.Commit()
}

func (s *Store) CurrentEvaluation(ctx context.Context, task, attempt string) (evaluation.Record, error) {
	history, err := s.EvaluationHistory(ctx, task, attempt)
	if err != nil {
		return evaluation.Record{}, err
	}
	return history[len(history)-1], nil
}

// EvaluationHistory returns the original followed by at most 100 revisions.
func (s *Store) EvaluationHistory(ctx context.Context, task, attempt string) ([]evaluation.Record, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	history, err := evaluationHistory(ctx, tx, task, attempt)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return history, nil
}

func evaluationHistory(ctx context.Context, tx *sql.Tx, task, attempt string) ([]evaluation.Record, error) {
	var base, head string
	var body []byte
	err := tx.QueryRowContext(ctx, `SELECT e.id,h.current_id,e.body FROM evaluations e JOIN evaluation_heads h ON h.base_id=e.id WHERE e.task_id=? AND e.attempt_id=?`, task, attempt).Scan(&base, &head, &body)
	if err != nil {
		return nil, err
	}
	var original evaluation.Record
	if json.Unmarshal(body, &original) != nil || original.Validate() != nil || original.ID != base || original.TaskID != task || original.AttemptID != attempt {
		return nil, evaluation.ErrEvidence
	}
	rows, err := tx.QueryContext(ctx, "SELECT id,supersedes,body FROM evaluation_revisions WHERE base_id=? ORDER BY rowid LIMIT 101", base)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	history := []evaluation.Record{original}
	for rows.Next() {
		var id, previous string
		var raw []byte
		if err := rows.Scan(&id, &previous, &raw); err != nil {
			return nil, err
		}
		var next evaluation.Record
		prior := history[len(history)-1]
		if len(history) > 100 || previous != prior.ID || json.Unmarshal(raw, &next) != nil || next.ID != id || evaluation.ValidateStoredRevision(prior, next) != nil {
			return nil, evaluation.ErrEvidence
		}
		history = append(history, next)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if history[len(history)-1].ID != head {
		return nil, evaluation.ErrEvidence
	}
	return history, nil
}
