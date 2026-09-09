// Package metrics defines bounded, identifier-free durable lifecycle metrics.
// Values are snapshot gauges, not process-local counters or quality judgments.
package metrics

import (
	"errors"
	"math"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/accounting"
	"github.com/ArronJablonowski/DarwinRouter/internal/stateschema"
)

var ErrInvalid = errors.New("invalid metrics snapshot")

const SnapshotVersion = 9

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
	Version           int                `json:"version"`
	ObservedAt        time.Time          `json:"observed_at"`
	StorageSchema     int                `json:"storage_schema"`
	Groups            []Group            `json:"groups"`
	TaskDuration      *TaskDuration      `json:"task_duration,omitempty"`
	OperationDuration *OperationDuration `json:"operation_duration,omitempty"`
	Resources         *Resources         `json:"resources,omitempty"`
	// Accounting contains identifier-free global retained-population totals.
	// It is unavailable on databases predating the immutable usage ledger.
	Accounting *accounting.Totals `json:"accounting,omitempty"`
}

type definition struct {
	name   string
	since  int
	states []string
}

var definitions = []definition{
	{"tasks", 1, []string{"running", "completed", "failed", "canceled"}},
	{"runtime_events", 1, []string{"task.started", "task.completed", "task.failed", "task.canceled", "turn.started", "turn.completed", "model.delta", "tool.started", "tool.completed", "worker.started", "worker.heartbeat", "worker.completed", "route.selected", "evaluation.recorded", "error.recorded", "steering.applied", "context.compacted"}},
	{"runtime_operations", 1, []string{"fallback", "compaction", "skill_context", "exploration", "capacity_exclusion", "budget_exclusion", "privacy_exclusion", "health_exclusion"}},
	{"submissions", 12, []string{"queued", "running", "succeeded", "failed", "canceled"}},
	{"queue_age", 12, []string{"lt_1s", "lt_10s", "lt_1m", "lt_5m", "lt_30m", "lt_1h", "gte_1h", "invalid_time"}},
	{"reviews", 7, []string{"started", "completed", "failed"}},
	{"evaluations", 2, []string{"stored"}},
	{"audits", 5, []string{"stored"}},
	{"audit_outcomes", 5, []string{"accept", "reject", "abstain"}},
	{"recoveries", 13, []string{"stored"}},
}

// NewSnapshot initializes canonical groups and explicit zero values. An older
// schema represents unavailable groups with available=false and no counts,
// rather than inventing measurements for features absent from that database.
// Callers must validate the populated snapshot before releasing it.
func NewSnapshot(schema int, at time.Time) Snapshot {
	s := Snapshot{Version: SnapshotVersion, ObservedAt: at, StorageSchema: schema, Groups: []Group{}}
	for _, def := range definitions {
		g := Group{Name: def.name, Available: schema >= def.since, Counts: []Count{}}
		if g.Available {
			for _, state := range def.states {
				g.Counts = append(g.Counts, Count{State: state})
			}
		}
		s.Groups = append(s.Groups, g)
	}
	if schema >= 29 {
		s.TaskDuration = newTaskDuration(at)
	}
	if schema >= 28 {
		s.OperationDuration = newOperationDuration(at)
	}
	if schema >= 30 {
		s.Accounting = &accounting.Totals{Version: 1, Coverage: accounting.CompleteCoverage, CalculatedAt: at.UTC()}
	}
	return s
}

// Validate restricts every group and state to a fixed vocabulary: callers cannot
// leak model names, task IDs, secret-bearing errors or arbitrary label values.
// Canonical order also makes snapshots deterministic apart from observation time.
func (s Snapshot) Validate() error {
	if s.Version != SnapshotVersion || s.StorageSchema < 1 || s.StorageSchema > stateschema.Current || s.ObservedAt.IsZero() || s.ObservedAt.Year() < 1 || s.ObservedAt.Year() > 9999 || len(s.Groups) != len(definitions) {
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
	if s.StorageSchema >= 12 {
		var queued, classified int64
		for _, group := range s.Groups {
			switch group.Name {
			case "submissions":
				queued = group.Counts[0].Value
			case "queue_age":
				for _, count := range group.Counts {
					if classified > math.MaxInt64-count.Value {
						return ErrInvalid
					}
					classified += count.Value
				}
			}
		}
		if queued != classified {
			return ErrInvalid
		}
	}
	if s.StorageSchema < 29 {
		if s.TaskDuration != nil {
			return ErrInvalid
		}
	} else if s.validateTaskDuration() != nil {
		return ErrInvalid
	}
	if s.StorageSchema < 28 {
		if s.OperationDuration != nil {
			return ErrInvalid
		}
	} else if s.validateOperationDuration() != nil {
		return ErrInvalid
	}
	if s.Resources != nil && s.validateResources() != nil {
		return ErrInvalid
	}
	if s.StorageSchema < 30 {
		if s.Accounting != nil {
			return ErrInvalid
		}
	} else if s.Accounting == nil || s.Accounting.Validate() != nil || s.Accounting.Scope.TaskID != "" || s.Accounting.Scope.SessionID != "" || s.Accounting.CalculatedAt.After(s.ObservedAt) {
		return ErrInvalid
	}
	return nil
}
