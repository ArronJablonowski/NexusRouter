package metrics

import (
	"bytes"
	"encoding/json"
	"math"
	"reflect"
	"testing"
	"time"
)

func TestTaskDurationCanonicalValidation(t *testing.T) {
	for name, mutate := range map[string]func(*Snapshot){
		"missing":         func(s *Snapshot) { s.TaskDuration = nil },
		"future start":    func(s *Snapshot) { s.TaskDuration.StartedAt = s.ObservedAt.Add(time.Nanosecond) },
		"pre epoch":       func(s *Snapshot) { s.TaskDuration.StartedAt = time.Unix(-1, 0) },
		"nan":             func(s *Snapshot) { s.TaskDuration.Groups[0].SumSeconds = math.NaN() },
		"inf":             func(s *Snapshot) { s.TaskDuration.Groups[0].SumSeconds = math.Inf(1) },
		"negative sum":    func(s *Snapshot) { s.TaskDuration.Groups[0].SumSeconds = -1 },
		"empty count sum": func(s *Snapshot) { s.TaskDuration.Groups[0].SumSeconds = 1 },
		"bound":           func(s *Snapshot) { s.TaskDuration.BoundsSeconds[0] = .2 },
		"state":           func(s *Snapshot) { s.TaskDuration.Groups[0].State = "private-task" },
		"reason":          func(s *Snapshot) { s.TaskDuration.Groups[0].Unavailable[0].State = "private-reason" },
		"bucket count":    func(s *Snapshot) { s.TaskDuration.Groups[0].BucketCounts = nil },
		"negative bucket": func(s *Snapshot) { s.TaskDuration.Groups[0].BucketCounts[0] = -1 },
		"negative count":  func(s *Snapshot) { s.TaskDuration.Groups[0].Count = -1 },
		"unreconciled":    func(s *Snapshot) { s.Groups[0].Counts[1].Value = 1 },
		"bucket overflow": func(s *Snapshot) {
			s.TaskDuration.Groups[0].BucketCounts[0] = math.MaxInt64
			s.TaskDuration.Groups[0].BucketCounts[1] = 1
		},
		"unavailable overflow": func(s *Snapshot) {
			s.TaskDuration.Groups[0].Unavailable[0].Value = math.MaxInt64
			s.TaskDuration.Groups[0].Unavailable[1].Value = 1
		},
		"epoch overflow": func(s *Snapshot) { s.ObservedAt = time.Unix(18_446_744_074, 0) },
	} {
		t.Run(name, func(t *testing.T) {
			s := NewSnapshot(29, time.Unix(1800000000, 0).UTC())
			mutate(&s)
			if s.Validate() == nil {
				t.Fatal("invalid duration accepted")
			}
		})
	}
}

func TestTaskDurationOwnedThresholdsAndWire(t *testing.T) {
	at := time.Unix(1800000000, 1).UTC()
	s := NewSnapshot(29, at)
	s.TaskDuration.StartedAt = at.Add(-time.Hour)
	g := &s.TaskDuration.Groups[0]
	for i, b := range TaskDurationBounds() {
		g.BucketCounts[i] = 1
		g.Count++
		g.SumSeconds += b.Seconds()
	}
	g.BucketCounts[10] = 1
	g.Count++
	g.SumSeconds += 3601
	g.Unavailable[0].Value = 2
	g.Unavailable[1].Value = 3
	s.Groups[0].Counts[1].Value = g.Count + 5
	if s.Validate() != nil {
		t.Fatal("canonical populated snapshot rejected")
	}
	before, _ := json.Marshal(s)
	body, err := MarshalOTLP(s)
	if err != nil {
		t.Fatal(err)
	}
	var request otlpRequest
	if json.Unmarshal(body, &request) != nil {
		t.Fatal("wire parse")
	}
	items := request.ResourceMetrics[0].ScopeMetrics[0].Metrics
	histogramIndex := len(s.Groups)
	if len(items) != histogramIndex+4 || items[histogramIndex].Gauge != nil || items[histogramIndex].Histogram == nil || items[histogramIndex].Histogram.AggregationTemporality != 2 || items[histogramIndex].Unit != "s" {
		t.Fatal("invalid histogram")
	}
	p := items[histogramIndex].Histogram.DataPoints[0]
	if len(items[histogramIndex].Histogram.DataPoints) != 3 || p.Count != "11" || p.StartTimeUnixNano != "1799996400000000001" || p.TimeUnixNano != "1800000000000000001" || p.Sum != g.SumSeconds || !reflect.DeepEqual(p.ExplicitBounds, s.TaskDuration.BoundsSeconds) {
		t.Fatal(p)
	}
	for _, c := range p.BucketCounts {
		if c != "1" {
			t.Fatal("bucket precision")
		}
	}
	if len(items[histogramIndex+1].Gauge.DataPoints) != 6 || items[histogramIndex+1].Gauge.DataPoints[0].AsInt != "2" || items[histogramIndex+1].Gauge.DataPoints[1].AsInt != "3" {
		t.Fatal("missing unavailable observations")
	}
	legacy := NewSnapshot(28, at)
	legacy.Groups = s.Groups
	oldBody, _ := MarshalOTLP(legacy)
	var old otlpRequest
	_ = json.Unmarshal(oldBody, &old)
	if !reflect.DeepEqual(items[:histogramIndex], old.ResourceMetrics[0].ScopeMetrics[0].Metrics[:histogramIndex]) {
		t.Fatal("existing gauges changed")
	}
	after, _ := json.Marshal(s)
	if !bytes.Equal(before, after) {
		t.Fatal("input mutated")
	}
	bounds := TaskDurationBounds()
	bounds[0] = 0
	s.TaskDuration.BoundsSeconds[0] = 0
	if TaskDurationBounds()[0] != 100*time.Millisecond || NewSnapshot(29, at).Validate() != nil {
		t.Fatal("shared bounds")
	}
	encoded, _ := json.Marshal(legacy)
	if bytes.Contains(encoded, []byte("task_duration")) {
		t.Fatal("legacy serialization changed")
	}
	legacy.TaskDuration = newTaskDuration(at)
	if legacy.Validate() == nil {
		t.Fatal("legacy fabricated duration")
	}
}

func TestTaskDurationMaximumCountAndEpoch(t *testing.T) {
	at := time.Unix(18_446_744_073, 709551615).UTC()
	s := NewSnapshot(29, at)
	s.TaskDuration.StartedAt = time.Unix(0, 0).UTC()
	s.Groups[0].Counts[1].Value = math.MaxInt64
	s.TaskDuration.Groups[0].Count = math.MaxInt64
	s.TaskDuration.Groups[0].BucketCounts[0] = math.MaxInt64
	body, err := MarshalOTLP(s)
	if err != nil || !bytes.Contains(body, []byte(`"count":"9223372036854775807"`)) || !bytes.Contains(body, []byte(`"startTimeUnixNano":"0"`)) {
		t.Fatal(err)
	}
}

func TestTaskDurationSumMatchesBucketRange(t *testing.T) {
	for _, tc := range []struct {
		name   string
		bucket int
		count  int64
		sum    float64
		valid  bool
	}{
		{"gross finite", 0, 1, 1e300, false},
		{"above first bucket", 0, 1, 1, false},
		{"below final bucket", 10, 1, 100, false},
		{"above duration maximum", 10, 1, 1e11, false},
		{"maximum duration", 10, 1, time.Duration(math.MaxInt64).Seconds(), true},
		{"upper rounding", 0, 3, math.Nextafter(.3, math.Inf(1)), true},
		{"lower rounding", 1, 3, math.Nextafter(.3, math.Inf(-1)), true},
		{"large count rounding", 0, math.MaxInt64, float64(math.MaxInt64) * .1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewSnapshot(29, time.Unix(1800000000, 0).UTC())
			s.Groups[0].Counts[1].Value = tc.count
			g := &s.TaskDuration.Groups[0]
			g.Count = tc.count
			g.BucketCounts[tc.bucket] = tc.count
			g.SumSeconds = tc.sum
			if (s.Validate() == nil) != tc.valid {
				t.Fatal("sum consistency", tc)
			}
		})
	}
}
