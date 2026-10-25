package telemetry

import (
	"context"
	"github.com/ArronJablonowski/NexusRouter/routing"
)

// CreativeOpinions contributes one current subjective rating per task. It does
// not fabricate execution reliability, cost, or deterministic success evidence.
func (s *Store) CreativeOpinions(ctx context.Context, key routing.Key, kind string) ([]routing.AdvisoryObservation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT a.task_id FROM browser_feedback a WHERE NOT EXISTS(SELECT 1 FROM browser_feedback b WHERE b.supersedes=a.id) AND json_valid(a.body) AND json_extract(a.body,'$.rating') BETWEEN 1 AND 10 AND json_extract(a.body,'$.model')=? AND json_extract(a.body,'$.provider')=? AND json_extract(a.body,'$.creative_kind')=? ORDER BY a.created_at DESC,a.id LIMIT 200`, key.Model, key.Provider, kind)
	if err != nil {
		return nil, err
	}
	var tasks []string
	for rows.Next() {
		var task string
		if err = rows.Scan(&task); err != nil {
			rows.Close()
			return nil, err
		}
		tasks = append(tasks, task)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := make([]routing.AdvisoryObservation, 0, len(tasks))
	for _, task := range tasks {
		history, err := s.BrowserFeedbackHistory(ctx, task)
		if err != nil {
			return nil, err
		}
		if len(history) == 0 {
			continue
		}
		r := history[len(history)-1]
		if r.Rating < 1 || r.Model != key.Model || r.Provider != key.Provider || r.CreativeKind != kind {
			continue
		}
		out = append(out, routing.AdvisoryObservation{ID: "creative-rating-" + r.ID, Key: key, Quality: float64(r.Rating-1) / 9, Confidence: 1, Time: r.CreatedAt})
	}
	return out, nil
}
