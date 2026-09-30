package metrics

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

func setRuntimeEventCount(s *Snapshot, state string, value int64) {
	for i := range s.Groups {
		if s.Groups[i].Name != "runtime_events" {
			continue
		}
		for j := range s.Groups[i].Counts {
			if s.Groups[i].Counts[j].State == state {
				s.Groups[i].Counts[j].Value = value
				return
			}
		}
	}
}

func TestOperationDurationValidationAndOTLP(t *testing.T) {
	at := time.Unix(1_800_000_000, 0).UTC()
	s := NewSnapshot(28, at)
	s.OperationDuration.StartedAt = at.Add(-time.Hour)
	provider := &s.OperationDuration.Groups[0]
	provider.Count, provider.SumSeconds, provider.BucketCounts[0] = 1, .1, 1
	provider.Unavailable[0].Value = 2
	provider.Unavailable[1].Value = 3
	provider.Unavailable[2].Value = 4
	setRuntimeEventCount(&s, "turn.started", 8)
	setRuntimeEventCount(&s, "turn.completed", 7)
	if s.Validate() != nil {
		t.Fatal(s)
	}
	body, err := MarshalOTLP(s)
	if err != nil {
		t.Fatal(err)
	}
	var request otlpRequest
	if json.Unmarshal(body, &request) != nil {
		t.Fatal(string(body))
	}
	items := request.ResourceMetrics[0].ScopeMetrics[0].Metrics
	if items[len(items)-2].Name != "nexusrouter.operation.duration" || items[len(items)-2].Histogram.DataPoints[0].Count != "1" || items[len(items)-1].Name != "nexusrouter.operation.duration.unavailable" || len(items[len(items)-1].Gauge.DataPoints) != 6 {
		t.Fatal(items[len(items)-2:])
	}

	mutations := map[string]func(*Snapshot){
		"missing":        func(s *Snapshot) { s.OperationDuration = nil },
		"future":         func(s *Snapshot) { s.OperationDuration.StartedAt = s.ObservedAt.Add(time.Second) },
		"bounds":         func(s *Snapshot) { s.OperationDuration.BoundsSeconds[0] = 1 },
		"group":          func(s *Snapshot) { s.OperationDuration.Groups[0].State = "private" },
		"bucket":         func(s *Snapshot) { s.OperationDuration.Groups[0].BucketCounts = nil },
		"sum":            func(s *Snapshot) { s.OperationDuration.Groups[0].SumSeconds = math.NaN() },
		"reason":         func(s *Snapshot) { s.OperationDuration.Groups[0].Unavailable[0].State = "private" },
		"start mismatch": func(s *Snapshot) { setRuntimeEventCount(s, "turn.started", 9) },
		"end mismatch":   func(s *Snapshot) { setRuntimeEventCount(s, "turn.completed", 8) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			var bad Snapshot
			body, _ := json.Marshal(s)
			if json.Unmarshal(body, &bad) != nil {
				t.Fatal("clone")
			}
			mutate(&bad)
			if bad.Validate() == nil {
				t.Fatal("invalid operation duration accepted")
			}
		})
	}
	legacy := NewSnapshot(27, at)
	if legacy.OperationDuration != nil || legacy.Validate() != nil {
		t.Fatal(legacy)
	}
}
