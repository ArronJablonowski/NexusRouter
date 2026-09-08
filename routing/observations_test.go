package routing

import (
	"encoding/json"
	"errors"
	"math"
	"math/rand"
	"reflect"
	"testing"
	"time"
)

func observationFixture() (Key, time.Time, Policy) {
	key := Key{Model: "model", Provider: "provider", Domain: "code", Profile: "default"}
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	p := Defaults()
	p.HalfLife = 10 * time.Hour
	p.MinSamples = 2
	p.Exploration = 0
	return key, now, p
}

func TestAggregateEvidenceMixedAgeAndEffectiveWindow(t *testing.T) {
	key, now, p := observationFixture()
	passed, failed := true, false
	set := ObservationSet{
		Fitness: []FitnessObservation{
			{ID: "new", BaseID: "new", Key: key, Quality: 1, Compliance: &passed, Reliability: true, Latency: time.Second, Cost: .1, Time: now},
			{ID: "old", BaseID: "old", Key: key, Quality: 0, Compliance: &failed, Latency: 3 * time.Second, Cost: .3, Time: now.Add(-p.HalfLife)},
		},
		Advisory: []AdvisoryObservation{
			{ID: "audit-new", Key: key, Quality: 1, Confidence: .8, Time: now},
			{ID: "audit-old", Key: key, Quality: 0, Confidence: .4, Time: now.Add(-p.HalfLife)},
		},
		Validity: []ValidityObservation{
			{ID: "valid-new", Key: key, Time: now},
			{ID: "invalid-old", Key: key, Failed: true, Time: now.Add(-p.HalfLife)},
		},
	}
	e, err := AggregateEvidence(key, set, now, p)
	if err != nil {
		t.Fatal(err)
	}
	if e.Samples != 2 || e.EffectiveSamples != 1.5 || e.DecayContribution != .75 || e.Quality != 2.0/3 || e.Compliance != 2.0/3 || e.Reliability != 2.0/3 || e.Latency != 1666666666*time.Nanosecond || e.Cost != 1.0/6 {
		t.Fatalf("fitness: %+v", e)
	}
	if !e.WindowStart.Equal(now.Add(-p.HalfLife)) || !e.WindowEnd.Equal(now) || !e.Updated.Equal(now) {
		t.Fatalf("fitness window: %+v", e)
	}
	if e.Advisory.Samples != 2 || e.Advisory.EffectiveSamples != 1.5 || e.Advisory.DecayContribution != .75 || math.Abs(e.Advisory.Quality-.8) > 1e-15 || math.Abs(e.Advisory.Confidence-2.0/3) > 1e-15 {
		t.Fatalf("advisory: %+v", e.Advisory)
	}
	if e.Validity.Samples != 2 || e.Validity.Failures != 1 || e.Validity.EffectiveSamples != 1.5 || e.Validity.EffectiveFailures != .5 || e.Validity.DecayContribution != .75 {
		t.Fatalf("validity: %+v", e.Validity)
	}

	r, candidates, _ := fixture()
	r.Domain, r.Profile = key.Domain, key.Profile
	candidates[0].Model, candidates[0].Provider = key.Model, key.Provider
	out, err := Select(r, p, candidates[:1], map[Key]Evidence{key: e}, now, .5)
	if err != nil || out.Primary.EffectiveSamples != 1.5 || out.Primary.Confidence != .75 || out.Primary.Uncertainty != .25 || out.Primary.DecayContribution != .75 || !out.Primary.WindowStart.Equal(e.WindowStart) || out.Primary.AdvisoryEffectiveSamples != 1.5 || out.Primary.ValidityEffectiveSamples != 1.5 {
		t.Fatalf("ranked explanation: %+v %v", out, err)
	}
}

func TestAggregateAdvisoryDirectionUsesReportedConfidence(t *testing.T) {
	key, now, p := observationFixture()
	set := ObservationSet{Advisory: []AdvisoryObservation{
		{ID: "weak-accept", Key: key, Quality: 1, Confidence: .01, Time: now},
		{ID: "strong-reject", Key: key, Quality: 0, Confidence: .99, Time: now},
	}}
	e, err := AggregateEvidence(key, set, now, p)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(e.Advisory.Quality-.01) > 1e-15 || e.Advisory.Confidence != .5 || e.Advisory.EffectiveSamples != 2 {
		t.Fatalf("advisory confidence lost: %+v", e.Advisory)
	}
}

func TestAggregateEvidenceOrderingAndDuplicateReplayInvariant(t *testing.T) {
	key, now, p := observationFixture()
	base := ObservationSet{
		Fitness: []FitnessObservation{
			{ID: "a", BaseID: "a", Key: key, Quality: .1, Reliability: true, Time: now.Add(-7 * time.Hour)},
			{ID: "b", BaseID: "b", Key: key, Quality: .9, Time: now.Add(-2 * time.Hour)},
			{ID: "c", BaseID: "c", Key: key, Quality: .4, Reliability: true, Time: now.Add(-5 * time.Hour)},
		},
		Advisory: []AdvisoryObservation{{ID: "x", Key: key, Quality: .2, Confidence: .3, Time: now.Add(-4 * time.Hour)}, {ID: "y", Key: key, Quality: .8, Confidence: .7, Time: now.Add(-time.Hour)}},
		Validity: []ValidityObservation{{ID: "m", Key: key, Failed: true, Time: now.Add(-6 * time.Hour)}, {ID: "n", Key: key, Time: now.Add(-3 * time.Hour)}},
	}
	want, err := AggregateEvidence(key, base, now, p)
	if err != nil {
		t.Fatal(err)
	}
	for seed := int64(0); seed < 100; seed++ {
		gotSet := ObservationSet{Fitness: append([]FitnessObservation(nil), base.Fitness...), Advisory: append([]AdvisoryObservation(nil), base.Advisory...), Validity: append([]ValidityObservation(nil), base.Validity...)}
		r := rand.New(rand.NewSource(seed))
		r.Shuffle(len(gotSet.Fitness), func(i, j int) { gotSet.Fitness[i], gotSet.Fitness[j] = gotSet.Fitness[j], gotSet.Fitness[i] })
		r.Shuffle(len(gotSet.Advisory), func(i, j int) { gotSet.Advisory[i], gotSet.Advisory[j] = gotSet.Advisory[j], gotSet.Advisory[i] })
		r.Shuffle(len(gotSet.Validity), func(i, j int) { gotSet.Validity[i], gotSet.Validity[j] = gotSet.Validity[j], gotSet.Validity[i] })
		gotSet.Fitness = append(gotSet.Fitness, gotSet.Fitness[0])
		gotSet.Advisory = append(gotSet.Advisory, gotSet.Advisory[0])
		gotSet.Validity = append(gotSet.Validity, gotSet.Validity[0])
		got, err := AggregateEvidence(key, gotSet, now, p)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("seed %d: got %+v want %+v err=%v", seed, got, want, err)
		}
	}
}

func TestAggregateEvidenceDecayIsMonotonicAndBounded(t *testing.T) {
	key, now, p := observationFixture()
	set := ObservationSet{Fitness: []FitnessObservation{{ID: "one", BaseID: "one", Key: key, Quality: 1, Reliability: true, Time: now}}}
	last := math.Inf(1)
	for halves := 0; halves <= 20; halves++ {
		e, err := AggregateEvidence(key, set, now.Add(time.Duration(halves)*p.HalfLife), p)
		if err != nil || e.EffectiveSamples > last || e.EffectiveSamples < 0 || e.DecayContribution < 0 || e.DecayContribution > 1 {
			t.Fatalf("halves=%d evidence=%+v err=%v", halves, e, err)
		}
		want := math.Exp2(-float64(halves))
		if e.EffectiveSamples != want || e.DecayContribution != want {
			t.Fatalf("halves=%d effective=%g want=%g", halves, e.EffectiveSamples, want)
		}
		last = e.EffectiveSamples
	}
	e, err := AggregateEvidence(key, set, now.Add(20000*p.HalfLife), p)
	if err != nil || e.EffectiveSamples != 0 || e.DecayContribution != 0 || e.Quality != .5 || e.Reliability != .5 {
		t.Fatalf("underflow boundary: %+v %v", e, err)
	}
}

func TestAggregateEvidenceCorrectionReplacesOneContribution(t *testing.T) {
	key, now, p := observationFixture()
	base := FitnessObservation{ID: "base", BaseID: "base", Key: key, Quality: 0, Reliability: true, Latency: time.Second, Cost: .2, Time: now.Add(-time.Hour)}
	revision := base
	revision.ID, revision.Supersedes, revision.Quality = "revision", "base", 1
	set := ObservationSet{Fitness: []FitnessObservation{revision, base, revision}}
	e, err := AggregateEvidence(key, set, now, p)
	if err != nil || e.Samples != 1 || e.Quality != 1 || e.Reliability != 1 || e.Latency != time.Second || e.Cost != .2 {
		t.Fatalf("corrected aggregate: %+v %v", e, err)
	}

	fork := revision
	fork.ID, fork.Quality = "fork", .5
	mutated := revision
	mutated.ID, mutated.Supersedes, mutated.Time = "mutated", "revision", revision.Time.Add(time.Second)
	for name, observations := range map[string][]FitnessObservation{
		"fork":              {base, revision, fork},
		"changed fact":      {base, revision, mutated},
		"orphan correction": {revision},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := AggregateEvidence(key, ObservationSet{Fitness: observations}, now, p); !errors.Is(err, ErrInvalid) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestAggregateEvidenceResolverAndClockBoundaries(t *testing.T) {
	key, now, p := observationFixture()
	calls := 0
	resolver := DecayResolverFunc(func(got Key) (time.Duration, bool) {
		calls++
		if got != key {
			t.Fatalf("key: %+v", got)
		}
		return 2 * time.Hour, true
	})
	local := now.In(time.FixedZone("offset", -6*60*60))
	set := ObservationSet{Fitness: []FitnessObservation{{ID: "one", BaseID: "one", Key: key, Quality: 1, Time: local.Add(-2 * time.Hour)}}}
	e, err := AggregateEvidence(key, set, now, p, resolver)
	if err != nil || calls != 1 || e.EffectiveSamples != .5 {
		t.Fatalf("evidence=%+v calls=%d err=%v", e, calls, err)
	}
	set.Fitness[0].Time = now.Add(time.Nanosecond)
	if _, err := AggregateEvidence(key, set, now, p); !errors.Is(err, ErrInvalid) {
		t.Fatalf("future observation: %v", err)
	}
	resolver = DecayResolverFunc(func(Key) (time.Duration, bool) { return 0, true })
	if _, err := AggregateEvidence(key, ObservationSet{}, now, p, resolver); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid override: %v", err)
	}
}

func TestDecayedDirectAndObjectiveEvidenceBoundAdvisory(t *testing.T) {
	key, now, p := observationFixture()
	p.Weights = Weights{Quality: 1}
	set := ObservationSet{
		Fitness:  []FitnessObservation{{ID: "user", BaseID: "user", Key: key, Quality: 0, Time: now}},
		Advisory: []AdvisoryObservation{{ID: "audit", Key: key, Quality: 1, Confidence: 1, Time: now}},
		Validity: []ValidityObservation{{ID: "validator", Key: key, Failed: true, Time: now}},
	}
	e, err := AggregateEvidence(key, set, now, p)
	if err != nil {
		t.Fatal(err)
	}
	r, candidates, _ := fixture()
	r.Domain, r.Profile = key.Domain, key.Profile
	candidates[0].Model, candidates[0].Provider = key.Model, key.Provider
	out, err := Select(r, p, candidates[:1], map[Key]Evidence{key: e}, now, .5)
	if err != nil {
		t.Fatal(err)
	}
	// One direct negative sample stays below neutral despite a maximally
	// positive audit. The deterministic invalidity result then reduces it; an
	// advisory never becomes a direct sample or bypasses objective evidence.
	if out.Primary.Score >= .5 || out.Primary.Samples != 1 || out.Primary.EffectiveSamples != 1 || out.Primary.AdvisorySamples != 1 || out.Primary.ValidityPenalty != .5 {
		t.Fatalf("precedence: %+v", out.Primary)
	}
}

func TestValidateExplanationCoversDecayProjection(t *testing.T) {
	key, now, p := observationFixture()
	e, err := AggregateEvidence(key, ObservationSet{Fitness: []FitnessObservation{{ID: "one", BaseID: "one", Key: key, Quality: 1, Time: now}}}, now, p)
	if err != nil {
		t.Fatal(err)
	}
	candidate := Candidate{Model: key.Model, Provider: key.Provider, Capabilities: []string{"chat"}, ContextTokens: 100, Healthy: true, PolicyAllowed: true, CapacityAvailable: true}
	selection, err := Select(Request{Mode: "hybrid", Domain: key.Domain, Profile: key.Profile, Capabilities: []string{"chat"}, ContextTokens: 1, MaxCost: 1}, p, []Candidate{candidate}, map[Key]Evidence{key: e}, now, .5)
	if err != nil || ValidateExplanation([]Candidate{candidate}, &p, &selection) != nil {
		t.Fatalf("valid projection: %+v %v", selection, err)
	}
	selection.Primary.EffectiveSamples = 2
	selection.Ranked[0].EffectiveSamples = 2
	if ValidateExplanation([]Candidate{candidate}, &p, &selection) == nil {
		t.Fatal("forged effective sample projection accepted")
	}
	selection, err = Select(Request{Mode: "hybrid", Domain: key.Domain, Profile: key.Profile, Capabilities: []string{"chat"}, ContextTokens: 1, MaxCost: 1}, p, []Candidate{candidate}, map[Key]Evidence{key: e}, now, .5)
	if err != nil {
		t.Fatal(err)
	}
	selection.Primary.Confidence = .25
	selection.Primary.Uncertainty = .75
	selection.Ranked[0] = selection.Primary
	if ValidateExplanation([]Candidate{candidate}, &p, &selection) == nil {
		t.Fatal("forged decay confidence accepted")
	}
}

func TestDecayProjectionPreservesLegacyJSONShapeWhenAbsent(t *testing.T) {
	legacy := struct {
		Ranked Ranked
	}{Ranked: Ranked{Model: "model", Provider: "provider", Score: .5, Confidence: .5, Recency: .5, Uncertainty: .5}}
	got, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"Ranked":{"ValiditySamples":0,"ValidityFailures":0,"ValidityPenalty":0,"AdvisorySamples":0,"AdvisoryInfluence":0,"Model":"model","Provider":"provider","FailureDomain":"","Score":0.5,"Confidence":0.5,"Recency":0.5,"Uncertainty":0.5,"Samples":0}}`
	if string(got) != want {
		t.Fatalf("legacy JSON changed:\n%s\nwant:\n%s", got, want)
	}
}
