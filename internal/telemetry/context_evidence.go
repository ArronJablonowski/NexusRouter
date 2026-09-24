package telemetry

import (
	"context"
	"encoding/json"

	"github.com/ArronJablonowski/DarwinRouter/contextpolicy"
	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/routing"
)

// ContextEvidence returns outcome summaries by allocated context tier. Records
// created before context attribution was introduced are intentionally omitted.
func (s *Store) ContextEvidence(ctx context.Context, model, provider string) ([]contextpolicy.Evidence, error) {
	return s.contextEvidence(ctx, model, provider, nil)
}

// ContextEvidenceFor keeps accuracy/latency task-specific while sharing hard
// resource faults across task categories for the same model/provider.
func (s *Store) ContextEvidenceFor(ctx context.Context, key routing.Key) ([]contextpolicy.Evidence, error) {
	if !validObservationKey(key) {
		return nil, routing.ErrInvalid
	}
	return s.contextEvidence(ctx, key.Model, key.Provider, &key)
}

func (s *Store) contextEvidence(ctx context.Context, model, provider string, key *routing.Key) ([]contextpolicy.Evidence, error) {
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
		if key == nil || (record.Key.Domain == key.Domain && record.Key.Profile == key.Profile) {
			item.Samples++
			if outcome.Accepted {
				item.Quality++
			}
			item.LatencyMillis += float64(record.Latency.Milliseconds())
		}
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
		if item.Samples > 0 {
			item.Quality /= float64(item.Samples)
			item.LatencyMillis /= float64(item.Samples)
		}
		out = append(out, *item)
	}
	return out, nil
}
