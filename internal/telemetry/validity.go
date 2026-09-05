package telemetry

import (
	"context"
	"encoding/json"
	"strings"

	"darwinrouter/evaluation"
	"darwinrouter/routing"
	"darwinrouter/runtime"
)

// OutputValidity counts mechanically checked final text, independently of
// subjective quality and fitness sample counts. Legacy tasks without the
// deterministic event do not imply either success or failure.
func (s *Store) OutputValidity(ctx context.Context, key routing.Key, requested ...string) (routing.Validity, error) {
	var out routing.Validity
	validationFilter := ""
	if len(requested) > 1 {
		return out, routing.ErrInvalid
	}
	if len(requested) == 1 {
		validationFilter = requested[0]
	}
	if validationFilter != "" && validationFilter != "go_source" {
		return out, routing.ErrInvalid
	}
	if key.Model == "" || key.Provider == "" || key.Domain == "" || key.Profile == "" {
		return out, routing.ErrInvalid
	}
	// Avoid examining unrelated history for cold candidates. For populated
	// candidates the query materializes a bounded recent set before fetching
	// output bodies or performing the expensive evidence consistency checks.
	var modelStarts int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM events
	 WHERE json_extract(body,'$.kind')='turn.started'
	 AND json_extract(body,'$.data.model_id')=? AND json_extract(body,'$.data.provider_id')=?`, key.Model, key.Provider).Scan(&modelStarts); err != nil {
		return out, err
	}
	if modelStarts == 0 {
		return out, nil
	}
	// For a dense model history, reverse event scanning can stop at the recent
	// window. For a sparse model, let SQLite start at the model index. This only
	// changes join planning, never filtering, ordering, or the sample limit.
	// Scope checks to the final turn's sequence window, then validate their
	// attempt identity below. Filtering only by attempt ID would silently hide
	// malformed final evidence. Steering after that turn invalidates its answer.
	join := "JOIN"
	if modelStarts > 200 {
		join = "CROSS JOIN"
	}
	rows, err := s.db.QueryContext(ctx, `WITH recent AS MATERIALIZED (
	 SELECT a.id,a.task_id,a.sequence,a.body,t.body AS start_body
	 FROM events a `+join+` task_heads h ON h.task_id=a.task_id AND h.state IN ('completed','failed')
	 `+join+` events t ON t.task_id=a.task_id AND json_extract(t.body,'$.kind')='turn.started'
	 AND t.sequence=(SELECT max(sequence) FROM events x WHERE x.task_id=a.task_id AND json_extract(x.body,'$.kind')='turn.started')
	 WHERE json_extract(a.body,'$.kind')='evaluation.recorded'
	 AND json_extract(a.body,'$.data.code') IN ('deterministic.nonempty_text.v1','deterministic.go_syntax.v1')
	 AND a.sequence=(SELECT min(sequence) FROM events x WHERE x.task_id=a.task_id AND x.sequence>t.sequence AND json_extract(x.body,'$.kind')='evaluation.recorded' AND json_extract(x.body,'$.data.code') IN ('deterministic.nonempty_text.v1','deterministic.go_syntax.v1'))
	 AND NOT EXISTS(SELECT 1 FROM events x WHERE x.task_id=a.task_id AND x.sequence>t.sequence AND json_extract(x.body,'$.kind')='steering.applied')
	 AND json_extract(t.body,'$.data.model_id')=? AND json_extract(t.body,'$.data.provider_id')=?
	 AND COALESCE((SELECT json_extract(p.body,'$.data.validation') FROM events p WHERE p.task_id=a.task_id AND json_extract(p.body,'$.kind')='task.started' LIMIT 1),'')=?
	 AND COALESCE((SELECT NULLIF(json_extract(p.body,'$.data.domain'),'') FROM events p WHERE p.task_id=a.task_id AND p.sequence<t.sequence AND json_extract(p.body,'$.kind')='route.selected' AND NULLIF(json_extract(p.body,'$.data.domain'),'') IS NOT NULL ORDER BY p.sequence DESC LIMIT 1),
	 (SELECT NULLIF(json_extract(p.body,'$.data.domain'),'') FROM events p WHERE p.task_id=a.task_id AND json_extract(p.body,'$.kind')='task.started' LIMIT 1),'general')=?
	 AND COALESCE((SELECT NULLIF(json_extract(p.body,'$.data.profile'),'') FROM events p WHERE p.task_id=a.task_id AND p.sequence<t.sequence AND json_extract(p.body,'$.kind')='route.selected' AND NULLIF(json_extract(p.body,'$.data.profile'),'') IS NOT NULL ORDER BY p.sequence DESC LIMIT 1),
	 (SELECT NULLIF(json_extract(p.body,'$.data.profile'),'') FROM events p WHERE p.task_id=a.task_id AND json_extract(p.body,'$.kind')='task.started' LIMIT 1),'default')=?
	 ORDER BY a.rowid DESC LIMIT 100)
	 SELECT a.id,a.task_id,a.sequence,a.body,a.start_body,
	 (SELECT body FROM events c WHERE c.task_id=a.task_id AND json_extract(c.body,'$.kind')='turn.completed' ORDER BY sequence DESC LIMIT 1),
	 (SELECT body FROM events z WHERE z.task_id=a.task_id ORDER BY sequence DESC LIMIT 1),
	 (SELECT body FROM events p WHERE p.task_id=a.task_id AND json_extract(p.body,'$.kind')='task.started' ORDER BY sequence LIMIT 1),
	 (SELECT body FROM events p WHERE p.task_id=a.task_id AND p.sequence>json_extract(a.start_body,'$.sequence') AND json_extract(p.body,'$.kind')='evaluation.recorded' AND json_extract(p.body,'$.data.code')='deterministic.go_syntax.v1' ORDER BY sequence LIMIT 1),
	 (SELECT count(*) FROM events d WHERE d.task_id=a.task_id AND d.sequence>json_extract(a.start_body,'$.sequence') AND json_extract(d.body,'$.kind')='evaluation.recorded' AND json_extract(d.body,'$.data.code')='deterministic.go_syntax.v1'),
	 (SELECT count(*) FROM events d WHERE d.task_id=a.task_id AND d.sequence>json_extract(a.start_body,'$.sequence') AND json_extract(d.body,'$.kind')='evaluation.recorded' AND json_extract(d.body,'$.data.code')='deterministic.nonempty_text.v1'),
	 (SELECT count(*) FROM events d WHERE d.task_id=a.task_id AND json_extract(d.body,'$.attempt_id')=json_extract(a.start_body,'$.attempt_id') AND json_extract(d.body,'$.kind')='turn.started'),
	 (SELECT count(*) FROM events d WHERE d.task_id=a.task_id AND json_extract(d.body,'$.attempt_id')=json_extract(a.start_body,'$.attempt_id') AND json_extract(d.body,'$.kind')='turn.completed')
	 FROM recent a`, key.Model, key.Provider, validationFilter, key.Domain, key.Profile)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, task string
		var sequence int64
		var evidence, started, completed, terminal, taskStarted, syntax []byte
		var checks, starts, ends, syntaxChecks int
		if err := rows.Scan(&id, &task, &sequence, &evidence, &started, &completed, &terminal, &taskStarted, &syntax, &syntaxChecks, &checks, &starts, &ends); err != nil {
			return routing.Validity{}, err
		}
		var events [5]runtime.Event
		for i, raw := range [][]byte{evidence, started, completed, terminal, taskStarted} {
			if json.Unmarshal(raw, &events[i]) != nil || events[i].Validate() != nil || events[i].TaskID != task {
				return routing.Validity{}, evaluation.ErrEvidence
			}
		}
		a, t, c, z := events[0], events[1], events[2], events[3]
		if checks != 1 || starts != 1 || ends != 1 || a.ID != id || a.Sequence != sequence || a.Kind != runtime.EvaluationRecorded || a.Data.Code != "deterministic.nonempty_text.v1" || a.Data.Accepted == nil || t.Kind != runtime.TurnStarted || c.Kind != runtime.TurnCompleted || t.AttemptID == "" || a.AttemptID != t.AttemptID || c.AttemptID != t.AttemptID || z.AttemptID != t.AttemptID || a.TurnID != t.TurnID || c.TurnID != t.TurnID || z.TurnID != t.TurnID || t.Sequence >= c.Sequence || c.Sequence >= a.Sequence || a.Sequence >= z.Sequence || len(c.Data.ToolCalls) != 0 || t.Data.ModelID != key.Model || t.Data.ProviderID != key.Provider || a.Data.ModelID != key.Model || a.Data.ProviderID != key.Provider {
			return routing.Validity{}, evaluation.ErrEvidence
		}
		validation := events[4].Data.Validation
		if events[4].Kind != runtime.TaskStarted || events[4].Sequence >= t.Sequence || (validation != "" && validation != "go_source") {
			return routing.Validity{}, evaluation.ErrEvidence
		}
		passed := strings.TrimSpace(c.Data.Text) != ""
		if *a.Data.Accepted != passed {
			return routing.Validity{}, evaluation.ErrEvidence
		}
		failureCode := "empty_output"
		updated := a.Time
		if validation == "go_source" && passed {
			var check runtime.Event
			if syntaxChecks != 1 || json.Unmarshal(syntax, &check) != nil || check.Validate() != nil || check.Kind != runtime.EvaluationRecorded || check.Data.Code != "deterministic.go_syntax.v1" || check.TaskID != task || check.AttemptID != t.AttemptID || check.TurnID != t.TurnID || check.Sequence <= a.Sequence || check.Sequence >= z.Sequence || check.Data.ModelID != key.Model || check.Data.ProviderID != key.Provider || check.Data.Accepted == nil {
				return routing.Validity{}, evaluation.ErrEvidence
			}
			passed = evaluation.GoSourceValid(c.Data.Text)
			if *check.Data.Accepted != passed {
				return routing.Validity{}, evaluation.ErrEvidence
			}
			failureCode = "invalid_output"
			if check.Time.After(updated) {
				updated = check.Time
			}
		} else if syntaxChecks != 0 {
			return routing.Validity{}, evaluation.ErrEvidence
		}
		if (passed && z.Kind != runtime.TaskCompleted) || (!passed && (z.Kind != runtime.TaskFailed || z.Data.Code != failureCode)) {
			return routing.Validity{}, evaluation.ErrEvidence
		}
		out.Samples++
		if !passed {
			out.Failures++
		}
		if updated.After(out.Updated) {
			out.Updated = updated
		}
	}
	if err := rows.Err(); err != nil {
		return routing.Validity{}, err
	}
	return out, nil
}
