package skills

import (
	"context"
	"sort"
	"time"
)

// LifecycleObservation is a content-free projection of one committed catalog
// transition. It intentionally omits skill, version, validator, evidence and
// operation identities. Draft generation lives in telemetry storage and is
// observed separately from catalog publication.
type LifecycleObservation struct {
	Kind string
	At   time.Time
}

// LifecycleObservations returns the newest committed activation and rollback
// transitions across the permitted scopes. Reading validates the complete
// catalog first and never loads version bodies or mutates catalog state.
func (s *FileStore) LifecycleObservations(ctx context.Context, limit int) ([]LifecycleObservation, error) {
	if s == nil || ctx == nil || limit < 1 || limit > 100 {
		return nil, ErrInvalid
	}
	out := make([]LifecycleObservation, 0)
	err := s.with(ctx, func(c *catalog) error {
		for _, entry := range c.Skills {
			if !s.scopes[entry.Key.Scope] {
				continue
			}
			for _, activation := range entry.Activations {
				kind := "activated"
				if activation.Rollback {
					kind = "rolled_back"
				}
				out = append(out, LifecycleObservation{Kind: kind, At: activation.At})
			}
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].At.Equal(out[j].At) {
				return out[i].Kind < out[j].Kind
			}
			return out[i].At.After(out[j].At)
		})
		if len(out) > limit {
			out = out[:limit]
		}
		return nil
	}, false)
	if err != nil {
		return nil, err
	}
	return out, nil
}
