package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"

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
	if ctx == nil || !validObservationKey(routing.Key{Model: model, Provider: provider, Domain: "context", Profile: "context"}) {
		return nil, routing.ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT e.id,e.task_id,e.attempt_id,e.domain,e.profile,
		CASE WHEN length(CAST(e.body AS BLOB)) BETWEEN 1 AND 262144 THEN e.body END,
		h.current_id,r.base_id,
		CASE WHEN length(CAST(r.body AS BLOB)) BETWEEN 1 AND 262144 THEN r.body END
		FROM evaluations e LEFT JOIN evaluation_heads h ON h.base_id=e.id
		LEFT JOIN evaluation_revisions r ON r.id=h.current_id
		WHERE e.model=? AND e.provider=?`, model, provider)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tiers := map[int]*contextpolicy.Evidence{}
	for rows.Next() {
		var id, task, attempt, domain, profile string
		var head, revisionBase sql.NullString
		var body, revisionBody []byte
		if err = rows.Scan(&id, &task, &attempt, &domain, &profile, &body, &head, &revisionBase, &revisionBody); err != nil {
			return nil, err
		}
		var record evaluation.Record
		storedKey := routing.Key{Model: model, Provider: provider, Domain: domain, Profile: profile}
		if !head.Valid || !validObservationKey(storedKey) || json.Unmarshal(body, &record) != nil || record.Validate() != nil || record.ID != id || record.TaskID != task || record.AttemptID != attempt || record.Key != storedKey {
			return nil, evaluation.ErrEvidence
		}
		if head.String != id {
			var revised evaluation.Record
			// Context/resource measurements are immutable across corrections.
			// Check the stored base binding before trusting the current verdict;
			// a missing or cross-linked head must not erase safety evidence.
			if !revisionBase.Valid || revisionBase.String != id || json.Unmarshal(revisionBody, &revised) != nil || revised.ID != head.String || evaluation.ValidateRevision(record, revised) != nil {
				return nil, evaluation.ErrEvidence
			}
			record = revised
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
		if outcome.Source != evaluation.Withdrawn && record.ExecutionSucceeded && !record.TimedOut && !record.ProviderError && (key == nil || (record.Key.Domain == key.Domain && record.Key.Profile == key.Profile)) {
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
	sort.Slice(out, func(i, j int) bool { return out[i].ContextTokens < out[j].ContextTokens })
	return out, nil
}
