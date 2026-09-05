package telemetry

import (
	"context"

	"darwinrouter/routing"
)

// AuditQuality reads advisory reviews without changing objective fitness. The
// latest inserted review supersedes prior reviews even when it abstains. Any
// persisted evaluation removes that attempt from the advisory population.
func (s *Store) AuditQuality(ctx context.Context, key routing.Key) (routing.Advisory, error) {
	var out routing.Advisory
	if key.Model == "" || key.Provider == "" || key.Domain == "" || key.Profile == "" {
		return out, routing.ErrInvalid
	}
	// Task-scoped indexes bound each correlated lookup. Filter in SQLite and
	// return at most 100 records; never load the full audit log into the process.
	rows, err := s.db.QueryContext(ctx, `SELECT a.id,a.task_id,a.body
	 FROM audit_records a JOIN events t ON t.task_id=a.task_id
	 AND json_extract(t.body,'$.kind')='turn.started'
	 AND json_extract(t.body,'$.attempt_id')=json_extract(a.body,'$.AttemptID')
	 WHERE json_extract(t.body,'$.data.model_id')=? AND json_extract(t.body,'$.data.provider_id')=?
	 AND json_extract(a.body,'$.Audit.domain')=?
	 AND COALESCE((SELECT json_extract(p.body,'$.data.profile') FROM events p
	  WHERE p.task_id=a.task_id AND p.sequence<=t.sequence AND json_extract(p.body,'$.kind')='route.selected'
	  AND NULLIF(json_extract(p.body,'$.data.profile'),'') IS NOT NULL ORDER BY p.sequence DESC LIMIT 1),
	 (SELECT json_extract(p.body,'$.data.profile') FROM events p WHERE p.task_id=a.task_id
	  AND json_extract(p.body,'$.kind')='task.started' AND NULLIF(json_extract(p.body,'$.data.profile'),'') IS NOT NULL LIMIT 1),'default')=?
	 AND NOT EXISTS(SELECT 1 FROM audit_records newer WHERE newer.task_id=a.task_id
	  AND newer.rowid>a.rowid AND json_extract(newer.body,'$.AttemptID')=json_extract(a.body,'$.AttemptID'))
	 AND NOT EXISTS(SELECT 1 FROM evaluations e WHERE e.task_id=a.task_id AND e.attempt_id=json_extract(a.body,'$.AttemptID'))
	 ORDER BY a.rowid DESC LIMIT 100`, key.Model, key.Provider, key.Domain, key.Profile)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	weight, accepted := 0.0, 0.0
	for rows.Next() {
		var id, task string
		var body []byte
		if err := rows.Scan(&id, &task, &body); err != nil {
			return routing.Advisory{}, err
		}
		r, err := decodeAuditRecord(body, id, task)
		if err != nil {
			return routing.Advisory{}, err
		}
		if r.Audit.Verdict == "abstain" || r.Audit.Confidence == 0 {
			continue
		}
		out.Samples++
		weight += r.Audit.Confidence
		if r.Audit.Verdict == "accept" {
			accepted += r.Audit.Confidence
		}
		if r.Time.After(out.Updated) {
			out.Updated = r.Time
		}
	}
	if err := rows.Err(); err != nil {
		return routing.Advisory{}, err
	}
	if out.Samples > 0 {
		out.Quality = accepted / weight
		out.Confidence = weight / float64(out.Samples)
	}
	return out, nil
}
