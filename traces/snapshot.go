// Package traces defines DarwinRouter's bounded, content-free trace export.
package traces

import (
	"errors"
	"time"
)

var ErrInvalid = errors.New("invalid trace snapshot")
var ErrExport = errors.New("trace export unavailable")

const SnapshotVersion = 1
const MaxTraces = 32
const MaxSpans = 512

type Snapshot struct {
	Version    int       `json:"version"`
	ObservedAt time.Time `json:"observed_at"`
	Traces     []Trace   `json:"traces"`
}

type Trace struct {
	Spans []Span `json:"spans"`
}

// Parent is -1 for the task root and otherwise indexes another span in the
// same trace. Names and outcomes use a closed vocabulary; event identities and
// content are deliberately absent.
type Span struct {
	Name      string    `json:"name"`
	Outcome   string    `json:"outcome"`
	Parent    int       `json:"parent"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`
}

func (s Snapshot) Validate() error {
	if s.Version != SnapshotVersion || s.ObservedAt.IsZero() || s.ObservedAt.Location() != time.UTC || len(s.Traces) > MaxTraces {
		return ErrInvalid
	}
	total := 0
	for _, trace := range s.Traces {
		if len(trace.Spans) == 0 || len(trace.Spans) > MaxSpans-total {
			return ErrInvalid
		}
		total += len(trace.Spans)
		root := trace.Spans[0]
		if root.Name != "task" || root.Parent != -1 || !taskOutcome(root.Outcome) || invalidTimes(root, s.ObservedAt) {
			return ErrInvalid
		}
		for i, span := range trace.Spans[1:] {
			if span.Parent != 0 || !spanVocabulary(span.Name, span.Outcome) || invalidTimes(span, s.ObservedAt) || span.StartedAt.Before(root.StartedAt) || span.EndedAt.After(root.EndedAt) {
				return ErrInvalid
			}
			if i > 0 && span.StartedAt.Before(trace.Spans[i].StartedAt) {
				return ErrInvalid
			}
		}
	}
	return nil
}

func spanVocabulary(name, outcome string) bool {
	switch name {
	case "provider", "tool", "worker":
		return outcome == "completed"
	case "route":
		return outcome == "selected" || outcome == "explored"
	case "route_constraint":
		return routeConstraint(outcome)
	case "evaluation":
		return outcome == "accepted" || outcome == "rejected"
	case "fallback":
		return outcome == "selected"
	case "compaction":
		return outcome == "applied"
	case "skill_context":
		return outcome == "loaded"
	case "steering":
		return outcome == "applied"
	case "error":
		return outcome == "recorded"
	default:
		return false
	}
}

func routeConstraint(value string) bool {
	switch value {
	case "mode", "privacy", "health", "policy", "credential", "capacity", "context", "budget", "capability":
		return true
	default:
		return false
	}
}

func invalidTimes(span Span, observedAt time.Time) bool {
	return span.StartedAt.IsZero() || span.EndedAt.IsZero() || span.StartedAt.Location() != time.UTC || span.EndedAt.Location() != time.UTC || span.EndedAt.Before(span.StartedAt) || span.EndedAt.After(observedAt)
}

func taskOutcome(value string) bool {
	return value == "completed" || value == "failed" || value == "canceled"
}
