package metrics

import (
	"math"
	"time"
)

type TaskDuration struct {
	StartedAt     time.Time       `json:"started_at"`
	BoundsSeconds []float64       `json:"bounds_seconds"`
	Groups        []DurationGroup `json:"groups"`
}

type DurationGroup struct {
	State        string  `json:"state"`
	Count        int64   `json:"count"`
	SumSeconds   float64 `json:"sum_seconds"`
	BucketCounts []int64 `json:"bucket_counts"`
	Unavailable  []Count `json:"unavailable"`
}

// TaskDurationBounds returns owned upper-inclusive histogram thresholds.
func TaskDurationBounds() []time.Duration {
	return []time.Duration{100 * time.Millisecond, 500 * time.Millisecond, time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second, time.Minute, 5 * time.Minute, 30 * time.Minute, time.Hour}
}

func newTaskDuration(at time.Time) *TaskDuration {
	d := &TaskDuration{StartedAt: at, BoundsSeconds: []float64{}, Groups: []DurationGroup{}}
	for _, b := range TaskDurationBounds() {
		d.BoundsSeconds = append(d.BoundsSeconds, b.Seconds())
	}
	for _, state := range []string{"completed", "failed", "canceled"} {
		d.Groups = append(d.Groups, DurationGroup{State: state, BucketCounts: make([]int64, 11), Unavailable: []Count{{State: "missing_start"}, {State: "invalid_time"}}})
	}
	return d
}

func epochNanos(at time.Time) (uint64, error) {
	if _, err := at.MarshalJSON(); err != nil {
		return 0, ErrInvalid
	}
	s, n := at.Unix(), uint64(at.Nanosecond())
	if s < 0 || uint64(s) > (math.MaxUint64-n)/1_000_000_000 {
		return 0, ErrInvalid
	}
	return uint64(s)*1_000_000_000 + n, nil
}

func (s Snapshot) validateTaskDuration() error {
	d := s.TaskDuration
	if d == nil || d.StartedAt.After(s.ObservedAt) || len(d.Groups) != 3 || len(d.BoundsSeconds) != 10 {
		return ErrInvalid
	}
	if _, err := epochNanos(d.StartedAt); err != nil {
		return err
	}
	if _, err := epochNanos(s.ObservedAt); err != nil {
		return err
	}
	for i, b := range TaskDurationBounds() {
		if d.BoundsSeconds[i] != b.Seconds() {
			return ErrInvalid
		}
	}
	for i, state := range []string{"completed", "failed", "canceled"} {
		g := d.Groups[i]
		if g.State != state || g.Count < 0 || g.SumSeconds < 0 || math.IsNaN(g.SumSeconds) || math.IsInf(g.SumSeconds, 0) || len(g.BucketCounts) != 11 || len(g.Unavailable) != 2 || g.Count == 0 && g.SumSeconds != 0 {
			return ErrInvalid
		}
		var total int64
		for _, c := range g.BucketCounts {
			if c < 0 || total > math.MaxInt64-c {
				return ErrInvalid
			}
			total += c
		}
		if total != g.Count {
			return ErrInvalid
		}
		if !durationSumConsistent(g, d.BoundsSeconds) {
			return ErrInvalid
		}
		for j, reason := range []string{"missing_start", "invalid_time"} {
			c := g.Unavailable[j]
			if c.State != reason || c.Value < 0 || total > math.MaxInt64-c.Value {
				return ErrInvalid
			}
			total += c.Value
		}
		if total != s.Groups[0].Counts[i+1].Value {
			return ErrInvalid
		}
	}
	return nil
}

// Duration samples are nonnegative int64 nanoseconds. Lower edges are treated
// inclusively here to avoid rejecting valid accumulated floating-point sums;
// actual bucket assignment remains upper-inclusive in the storage recorder.
func durationSumConsistent(g DurationGroup, bounds []float64) bool {
	var lower, upper float64
	for i, count := range g.BucketCounts {
		lo, hi := 0.0, time.Duration(math.MaxInt64).Seconds()
		if i > 0 {
			lo = bounds[i-1]
		}
		if i < len(bounds) {
			hi = bounds[i]
		}
		lower += float64(count) * lo
		upper += float64(count) * hi
	}
	// Relative slack covers accumulation and integer-to-double conversion;
	// nanosecond-scale absolute slack handles values near zero.
	lowerSlack := math.Max(1e-9, math.Abs(lower)*1e-12)
	upperSlack := math.Max(1e-9, math.Abs(upper)*1e-12)
	return g.SumSeconds >= lower-lowerSlack && g.SumSeconds <= upper+upperSlack
}
