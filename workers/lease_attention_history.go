package workers

import (
	"math"

	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

// LeaseAttentionTransition preserves an observation, not evidence authorizing
// resource release. A baseline represents only the state present at migration.
type LeaseAttentionTransition struct {
	Version     int            `json:"version"`
	Sequence    int64          `json:"sequence"`
	Kind        string         `json:"kind"`
	Observation LeaseAttention `json:"observation"`
}

func (t LeaseAttentionTransition) Validate() error {
	if t.Version != 1 || t.Sequence < 1 || t.Observation.Validate() != nil || (t.Kind != "observed" && (t.Kind != "baseline" || t.Sequence != 1)) {
		return ErrLeaseAttention
	}
	return nil
}

type LeaseAttentionHistoryOptions struct {
	AfterSequence int64 `json:"after_sequence"`
	Limit         int   `json:"limit"`
}

func (o LeaseAttentionHistoryOptions) Validate() error {
	if o.AfterSequence < 0 || o.AfterSequence > math.MaxInt64-101 || o.Limit < 1 || o.Limit > 100 {
		return ErrLeaseAttention
	}
	return nil
}

type LeaseAttentionHistoryPage struct {
	Version       int                        `json:"version"`
	StorageSchema int                        `json:"storage_schema"`
	Available     bool                       `json:"available"`
	AttentionID   string                     `json:"attention_id"`
	Items         []LeaseAttentionTransition `json:"items"`
	NextSequence  int64                      `json:"next_sequence"`
	HasMore       bool                       `json:"has_more"`
}

func (p LeaseAttentionHistoryPage) Validate() error {
	if p.Version != 1 || p.StorageSchema < 1 || p.StorageSchema > 28 || p.Available != (p.StorageSchema >= 25) || !sessions.ValidEventPageID(p.AttentionID) || p.Items == nil || len(p.Items) > 100 || p.NextSequence < 0 || p.HasMore != (p.NextSequence != 0) || (!p.Available && (len(p.Items) != 0 || p.HasMore)) {
		return ErrLeaseAttention
	}
	for i, item := range p.Items {
		if item.Validate() != nil || item.Observation.ID != p.AttentionID {
			return ErrLeaseAttention
		}
		if i > 0 {
			previous := p.Items[i-1]
			if previous.Sequence == math.MaxInt64 || item.Sequence != previous.Sequence+1 || item.Observation.TaskID != previous.Observation.TaskID || item.Observation.Writer != previous.Observation.Writer || !item.Observation.FirstObserved.Equal(previous.Observation.FirstObserved) || item.Observation.UpdatedAt.Before(previous.Observation.UpdatedAt) {
				return ErrLeaseAttention
			}
		}
	}
	if p.HasMore && (len(p.Items) == 0 || p.NextSequence != p.Items[len(p.Items)-1].Sequence) {
		return ErrLeaseAttention
	}
	return nil
}
