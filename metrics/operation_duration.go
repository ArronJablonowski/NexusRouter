package metrics

import (
	"math"
	"time"
)

// OperationDuration reports identifier-free provider-turn and tool-call
// latency from paired durable events. It is a cumulative retained-population
// view, not a live in-flight timer.
type OperationDuration struct {
	StartedAt     time.Time       `json:"started_at"`
	BoundsSeconds []float64       `json:"bounds_seconds"`
	Groups        []DurationGroup `json:"groups"`
}

func newOperationDuration(at time.Time) *OperationDuration {
	d := &OperationDuration{StartedAt: at, BoundsSeconds: []float64{}, Groups: []DurationGroup{}}
	for _, bound := range TaskDurationBounds() {
		d.BoundsSeconds = append(d.BoundsSeconds, bound.Seconds())
	}
	for _, state := range []string{"provider", "tool"} {
		d.Groups = append(d.Groups, DurationGroup{
			State: state, BucketCounts: make([]int64, len(d.BoundsSeconds)+1),
			Unavailable: []Count{{State: "missing_start"}, {State: "missing_end"}, {State: "invalid_time"}},
		})
	}
	return d
}

func (s Snapshot) validateOperationDuration() error {
	d := s.OperationDuration
	if d == nil || d.StartedAt.After(s.ObservedAt) || len(d.Groups) != 2 || len(d.BoundsSeconds) != len(TaskDurationBounds()) {
		return ErrInvalid
	}
	if _, err := epochNanos(d.StartedAt); err != nil {
		return ErrInvalid
	}
	for i, bound := range TaskDurationBounds() {
		if d.BoundsSeconds[i] != bound.Seconds() {
			return ErrInvalid
		}
	}
	starts, ends := operationEventCounts(s)
	for i, state := range []string{"provider", "tool"} {
		group := d.Groups[i]
		if group.State != state || group.Count < 0 || group.SumSeconds < 0 || math.IsNaN(group.SumSeconds) || math.IsInf(group.SumSeconds, 0) || len(group.BucketCounts) != len(d.BoundsSeconds)+1 || len(group.Unavailable) != 3 || group.Count == 0 && group.SumSeconds != 0 {
			return ErrInvalid
		}
		var observed int64
		for _, count := range group.BucketCounts {
			if count < 0 || observed > math.MaxInt64-count {
				return ErrInvalid
			}
			observed += count
		}
		if observed != group.Count || !durationSumConsistent(group, d.BoundsSeconds) {
			return ErrInvalid
		}
		for j, reason := range []string{"missing_start", "missing_end", "invalid_time"} {
			if group.Unavailable[j].State != reason || group.Unavailable[j].Value < 0 {
				return ErrInvalid
			}
		}
		missingStart, missingEnd, invalid := group.Unavailable[0].Value, group.Unavailable[1].Value, group.Unavailable[2].Value
		if addCounts(group.Count, missingEnd, invalid) != starts[i] || addCounts(group.Count, missingStart, invalid) != ends[i] {
			return ErrInvalid
		}
	}
	return nil
}

func addCounts(values ...int64) int64 {
	var total int64
	for _, value := range values {
		if value < 0 || total > math.MaxInt64-value {
			return -1
		}
		total += value
	}
	return total
}

func operationEventCounts(s Snapshot) ([2]int64, [2]int64) {
	var starts, ends [2]int64
	for _, group := range s.Groups {
		if group.Name != "runtime_events" || !group.Available {
			continue
		}
		for _, count := range group.Counts {
			switch count.State {
			case "turn.started":
				starts[0] = count.Value
			case "turn.completed":
				ends[0] = count.Value
			case "tool.started":
				starts[1] = count.Value
			case "tool.completed":
				ends[1] = count.Value
			}
		}
	}
	return starts, ends
}
