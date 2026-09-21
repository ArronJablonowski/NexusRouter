package telemetry

import (
	"context"
	"encoding/json"

	"github.com/ArronJablonowski/DarwinRouter/contextpolicy"
	"github.com/ArronJablonowski/DarwinRouter/evaluation"
)

// ContextEvidence returns outcome summaries by allocated context tier. Records
// created before context attribution was introduced are intentionally omitted.
func (s *Store) ContextEvidence(ctx context.Context, model, provider string) ([]contextpolicy.Evidence, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT CASE WHEN h.current_id=e.id THEN e.body ELSE r.body END
		FROM evaluations e JOIN evaluation_heads h ON h.base_id=e.id
		LEFT JOIN evaluation_revisions r ON r.id=h.current_id
		WHERE e.model=? AND e.provider=?`, model, provider)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tiers := map[int]*contextpolicy.Evidence{}
	for rows.Next() {
		var body []byte
		if err = rows.Scan(&body); err != nil {
			return nil, err
		}
		var record evaluation.Record
		if json.Unmarshal(body, &record) != nil || record.Validate() != nil {
			return nil, evaluation.ErrEvidence
		}
		if record.ContextTokens < 1 {
			continue
		}
		item := tiers[record.ContextTokens]
		if item == nil {
			item = &contextpolicy.Evidence{ContextTokens: record.ContextTokens}
			tiers[record.ContextTokens] = item
		}
		outcome, resolveErr := evaluation.Resolve(record.Checks, record.AllowJudge)
		if resolveErr != nil {
			return nil, resolveErr
		}
		item.Samples++
		if outcome.Accepted {
			item.Quality++
		}
		item.LatencyMillis += float64(record.Latency.Milliseconds())
		if record.TimedOut {
			item.Timeouts++
		}
		if record.ProviderError {
			item.ProviderErrors++
		}
		item.PeakMemory = max(item.PeakMemory, record.PeakMemoryBytes)
		item.MaxSwapGrowth = max(item.MaxSwapGrowth, record.SwapGrowthBytes)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	out := make([]contextpolicy.Evidence, 0, len(tiers))
	for _, item := range tiers {
		item.Quality /= float64(item.Samples)
		item.LatencyMillis /= float64(item.Samples)
		out = append(out, *item)
	}
	return out, nil
}
