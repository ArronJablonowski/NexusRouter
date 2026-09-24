package routing

import (
	"math"
	"testing"
	"time"
)

func TestAggregateEvidenceKeepsFiniteExecutionMeasurementsInRange(t *testing.T) {
	key, now, policy := observationFixture()
	set := ObservationSet{Fitness: []FitnessObservation{
		{ID: "a", BaseID: "a", Key: key, Quality: 1, Reliability: true, Cost: math.MaxFloat64, Latency: time.Duration(math.MaxInt64), Time: now},
		{ID: "b", BaseID: "b", Key: key, Quality: 1, Reliability: true, Cost: math.MaxFloat64, Latency: time.Duration(math.MaxInt64), Time: now},
	}}
	evidence, err := AggregateEvidence(key, set, now, policy)
	if err != nil || evidence.Cost != math.MaxFloat64 || evidence.Latency != time.Duration(math.MaxInt64) {
		t.Fatalf("valid measurements overflowed during aggregation: %+v %v", evidence, err)
	}
	request, candidates, _ := fixture()
	candidates[0].Model, candidates[0].Provider = key.Model, key.Provider
	if _, err = Select(request, policy, candidates[:1], map[Key]Evidence{key: evidence}, now, .5); err != nil {
		t.Fatalf("aggregate cannot be routed: %v", err)
	}
}
