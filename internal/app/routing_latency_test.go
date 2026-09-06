package app

import (
	"slices"
	"testing"
	"time"
)

// taskLatencyPercentile uses nearest rank on ordered observations. These are
// empirical samples, not interpolated values or confidence bounds. Sorting is
// performed after the benchmark timer stops by reportTaskLatencies.
func taskLatencyPercentile(ordered []time.Duration, percentile int) time.Duration {
	if len(ordered) == 0 || percentile < 1 || percentile > 100 {
		return 0
	}
	// ceil(n*p/100), avoiding n*p overflow for large benchmark sample counts.
	rank := len(ordered)/100*percentile + (len(ordered)%100*percentile+99)/100
	return ordered[rank-1]
}

func reportTaskLatencies(b *testing.B, observations []time.Duration) {
	b.Helper()
	if len(observations) != b.N || len(observations) == 0 {
		b.Fatal("latency observations must cover every timed task")
	}
	slices.Sort(observations)
	for _, item := range []struct {
		percentile int
		name       string
	}{{50, "p50-ms"}, {95, "p95-ms"}, {99, "p99-ms"}, {100, "max-ms"}} {
		b.ReportMetric(float64(taskLatencyPercentile(observations, item.percentile))/float64(time.Millisecond), item.name)
	}
	b.ReportMetric(float64(len(observations)), "samples")
}

func TestTaskLatencyNearestRank(t *testing.T) {
	for _, n := range []int{1, 3, 20, 100, 101, 1000} {
		ordered := make([]time.Duration, n)
		for i := range ordered {
			ordered[i] = time.Duration(i+1) * time.Millisecond
		}
		for _, p := range []int{1, 50, 95, 99, 100} {
			want := time.Duration((n*p+99)/100) * time.Millisecond
			if got := taskLatencyPercentile(ordered, p); got != want {
				t.Fatalf("n=%d p=%d got=%v want=%v", n, p, got, want)
			}
		}
	}
	if taskLatencyPercentile(nil, 50) != 0 || taskLatencyPercentile([]time.Duration{1}, 0) != 0 || taskLatencyPercentile([]time.Duration{1}, 101) != 0 {
		t.Fatal("invalid sample or percentile accepted")
	}
}
