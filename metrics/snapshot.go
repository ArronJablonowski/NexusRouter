// Package metrics defines bounded, identifier-free durable lifecycle metrics.
// Values are snapshot gauges, not process-local counters or quality judgments.
package metrics

import (
	"errors"
	"math"
	"time"
)

var ErrInvalid = errors.New("invalid metrics snapshot")

type Count struct {
	State string `json:"state"`
	Value int64  `json:"value"`
}

type Group struct {
	Name      string  `json:"name"`
	Available bool    `json:"available"`
	Counts    []Count `json:"counts"`
}

type Snapshot struct {
	Version       int       `json:"version"`
	ObservedAt    time.Time `json:"observed_at"`
	StorageSchema int       `json:"storage_schema"`
	Groups        []Group   `json:"groups"`
}

type definition struct {
	name   string
	since  int
	states []string
}

var definitions = []definition{
	{"tasks", 1, []string{"running", "completed", "failed", "canceled"}},
	{"submissions", 12, []string{"queued", "running", "succeeded", "failed", "canceled"}},
	{"reviews", 7, []string{"started", "completed", "failed"}},
	{"evaluations", 2, []string{"stored"}},
	{"audits", 5, []string{"stored"}},
	{"recoveries", 13, []string{"stored"}},
}

// NewSnapshot initializes canonical groups and explicit zero values. An older
// schema represents unavailable groups with available=false and no counts,
// rather than inventing measurements for features absent from that database.
// Callers must validate the populated snapshot before releasing it.
func NewSnapshot(schema int, at time.Time) Snapshot {
	s := Snapshot{Version: 1, ObservedAt: at, StorageSchema: schema, Groups: []Group{}}
	for _, def := range definitions {
		g := Group{Name: def.name, Available: schema >= def.since, Counts: []Count{}}
		if g.Available {
			for _, state := range def.states {
				g.Counts = append(g.Counts, Count{State: state})
			}
		}
		s.Groups = append(s.Groups, g)
	}
	return s
}

// Validate restricts every group and state to a fixed vocabulary: callers cannot
// leak model names, task IDs, secret-bearing errors or arbitrary label values.
// Canonical order also makes snapshots deterministic apart from observation time.
func (s Snapshot) Validate() error {
	if s.Version != 1 || s.StorageSchema < 1 || s.StorageSchema > 27 || s.ObservedAt.IsZero() || s.ObservedAt.Year() < 1 || s.ObservedAt.Year() > 9999 || len(s.Groups) != len(definitions) {
		return ErrInvalid
	}
	if _, err := s.ObservedAt.MarshalJSON(); err != nil {
		return ErrInvalid
	}
	for i, def := range definitions {
		g := s.Groups[i]
		if g.Name != def.name || g.Available != (s.StorageSchema >= def.since) || g.Counts == nil {
			return ErrInvalid
		}
		if !g.Available {
			if len(g.Counts) != 0 {
				return ErrInvalid
			}
			continue
		}
		if len(g.Counts) != len(def.states) {
			return ErrInvalid
		}
		var total int64
		for j, c := range g.Counts {
			if c.State != def.states[j] || c.Value < 0 || total > math.MaxInt64-c.Value {
				return ErrInvalid
			}
			total += c.Value
		}
	}
	return nil
}
