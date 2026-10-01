package remote

import (
	"testing"
	"time"
)

func TestRemoteObservationClockSkewBoundaries(t *testing.T) {
	now := time.Date(2026, 10, 1, 22, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		at   time.Time
		want bool
	}{
		{"zero", time.Time{}, false}, {"current", now, true},
		{"physical-spark-skew", now.Add(40 * time.Millisecond), true},
		{"maximum-skew", now.Add(time.Second), true},
		{"excessive-skew", now.Add(time.Second + time.Nanosecond), false},
		{"freshness-boundary", now.Add(-15 * time.Second), true},
		{"stale", now.Add(-15*time.Second - time.Nanosecond), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := freshObservationAt(tc.at, now); got != tc.want {
				t.Fatalf("fresh=%v want=%v", got, tc.want)
			}
		})
	}
}
