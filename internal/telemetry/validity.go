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
func (s *Store) OutputValidity(ctx context.Context, key routing.Key) (routing.Validity, error) {
	var out routing.Validity
	if key.Model == "" || key.Provider == "" || key.Domain == "" || key.Profile == "" {
		return out, routing.ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT a.id,a.task_id,a.sequence,a.body,t.body,
	 (SELECT body FROM events c WHERE c.task_id=a.task_id AND json_extract(c.body,'$.kind')='turn.completed' ORDER BY sequence DESC LIMIT 1),
	 (SELECT body FROM events z WHERE z.task_id=a.task_id ORDER BY sequence DESC LIMIT 1),
	 (SELECT count(*) FROM events d WHERE d.task_id=a.task_id AND json_extract(d.body,'$.kind')='evaluation.recorded' AND json_extract(d.body,'$.data.code')='deterministic.nonempty_text.v1'),
	 (SELECT count(*) FROM events d WHERE d.task_id=a.task_id AND json_extract(d.body,'$.attempt_id')=json_extract(t.body,'$.attempt_id') AND json_extract(d.body,'$.kind')='turn.started'),
	 (SELECT count(*) FROM events d WHERE d.task_id=a.task_id AND json_extract(d.body,'$.attempt_id')=json_extract(t.body,'$.attempt_id') AND json_extract(d.body,'$.kind')='turn.completed')
	 FROM events a JOIN task_heads h ON h.task_id=a.task_id AND h.state IN ('completed','failed')
	 JOIN events t ON t.task_id=a.task_id AND t.sequence=(SELECT max(sequence) FROM events x WHERE x.task_id=a.task_id AND json_extract(x.body,'$.kind')='turn.started')
	 WHERE json_extract(a.body,'$.kind')='evaluation.recorded' AND json_extract(a.body,'$.data.code')='deterministic.nonempty_text.v1'
	 AND json_extract(t.body,'$.data.model_id')=? AND json_extract(t.body,'$.data.provider_id')=?
	 AND COALESCE((SELECT NULLIF(json_extract(p.body,'$.data.domain'),'') FROM events p WHERE p.task_id=a.task_id AND p.sequence<t.sequence AND json_extract(p.body,'$.kind')='route.selected' AND NULLIF(json_extract(p.body,'$.data.domain'),'') IS NOT NULL ORDER BY p.sequence DESC LIMIT 1),
	 (SELECT NULLIF(json_extract(p.body,'$.data.domain'),'') FROM events p WHERE p.task_id=a.task_id AND json_extract(p.body,'$.kind')='task.started' LIMIT 1),'general')=?
	 AND COALESCE((SELECT NULLIF(json_extract(p.body,'$.data.profile'),'') FROM events p WHERE p.task_id=a.task_id AND p.sequence<t.sequence AND json_extract(p.body,'$.kind')='route.selected' AND NULLIF(json_extract(p.body,'$.data.profile'),'') IS NOT NULL ORDER BY p.sequence DESC LIMIT 1),
	 (SELECT NULLIF(json_extract(p.body,'$.data.profile'),'') FROM events p WHERE p.task_id=a.task_id AND json_extract(p.body,'$.kind')='task.started' LIMIT 1),'default')=?
	 ORDER BY a.rowid DESC LIMIT 100`, key.Model, key.Provider, key.Domain, key.Profile)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, task string
		var sequence int64
		var evidence, started, completed, terminal []byte
		var checks, starts, ends int
		if err := rows.Scan(&id, &task, &sequence, &evidence, &started, &completed, &terminal, &checks, &starts, &ends); err != nil {
			return routing.Validity{}, err
		}
		var events [4]runtime.Event
		for i, raw := range [][]byte{evidence, started, completed, terminal} {
			if json.Unmarshal(raw, &events[i]) != nil || events[i].Validate() != nil || events[i].TaskID != task {
				return routing.Validity{}, evaluation.ErrEvidence
			}
		}
		a, t, c, z := events[0], events[1], events[2], events[3]
		if checks != 1 || starts != 1 || ends != 1 || a.ID != id || a.Sequence != sequence || a.Kind != runtime.EvaluationRecorded || a.Data.Code != "deterministic.nonempty_text.v1" || a.Data.Accepted == nil || t.Kind != runtime.TurnStarted || c.Kind != runtime.TurnCompleted || t.AttemptID == "" || a.AttemptID != t.AttemptID || c.AttemptID != t.AttemptID || z.AttemptID != t.AttemptID || a.TurnID != t.TurnID || c.TurnID != t.TurnID || z.TurnID != t.TurnID || t.Sequence >= c.Sequence || c.Sequence >= a.Sequence || a.Sequence >= z.Sequence || len(c.Data.ToolCalls) != 0 || t.Data.ModelID != key.Model || t.Data.ProviderID != key.Provider || a.Data.ModelID != key.Model || a.Data.ProviderID != key.Provider {
			return routing.Validity{}, evaluation.ErrEvidence
		}
		passed := strings.TrimSpace(c.Data.Text) != ""
		if *a.Data.Accepted != passed || (passed && z.Kind != runtime.TaskCompleted) || (!passed && (z.Kind != runtime.TaskFailed || z.Data.Code != "empty_output")) {
			return routing.Validity{}, evaluation.ErrEvidence
		}
		out.Samples++
		if !passed {
			out.Failures++
		}
		if a.Time.After(out.Updated) {
			out.Updated = a.Time
		}
	}
	if err := rows.Err(); err != nil {
		return routing.Validity{}, err
	}
	return out, nil
}
