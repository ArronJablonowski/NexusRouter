package routing

import (
	"math"
	"sort"
	"time"
)

// DecayResolver supplies a half-life override for one complete execution key.
// A miss preserves the policy's global HalfLife. Implementations must be pure
// for a configuration snapshot; AggregateEvidence resolves it exactly once.
type DecayResolver interface {
	ResolveDecay(Key) (time.Duration, bool)
}

// DecayResolverFunc adapts a function to DecayResolver.
type DecayResolverFunc func(Key) (time.Duration, bool)

func (f DecayResolverFunc) ResolveDecay(key Key) (time.Duration, bool) { return f(key) }

// FitnessObservation is one immutable evaluation version. The base version has
// ID == BaseID and an empty Supersedes. A correction has a new ID and names its
// immediate predecessor. Corrections may change Quality only; execution facts
// and the source timestamp remain fixed. This lets replay resolve one current
// contribution per base observation without depending on insertion order.
type FitnessObservation struct {
	ID, BaseID, Supersedes string
	Key                    Key
	Quality                float64
	Compliance             *bool
	Reliability            bool
	Latency                time.Duration
	Cost                   float64
	Time                   time.Time
}

// AdvisoryObservation is immutable orchestrator-review evidence. Attempt-level
// precedence (direct user/deterministic evidence suppressing an advisory) is
// resolved by the evidence store before constructing this set.
type AdvisoryObservation struct {
	ID                  string
	Key                 Key
	Quality, Confidence float64
	Time                time.Time
}

// ValidityObservation is immutable objective output-validity evidence.
type ValidityObservation struct {
	ID     string
	Key    Key
	Failed bool
	Time   time.Time
}

type ObservationSet struct {
	Fitness  []FitnessObservation
	Advisory []AdvisoryObservation
	Validity []ValidityObservation
}

// AggregateEvidence applies event-time exponential decay independently to each
// current observation. now is the sole clock input. Observations after now are
// rejected rather than silently clamped; exact duplicate replay is idempotent.
// Late arrival and backfill therefore produce the same result as any other
// ordering of the same immutable records.
func AggregateEvidence(key Key, set ObservationSet, now time.Time, policy Policy, overrides ...DecayResolver) (Evidence, error) {
	if key.Model == "" || key.Provider == "" || key.Domain == "" || key.Profile == "" || now.IsZero() || policy.MinSamples < 1 {
		return Evidence{}, ErrInvalid
	}
	if len(overrides) > 1 {
		return Evidence{}, ErrInvalid
	}
	halfLife := policy.HalfLife
	if len(overrides) == 1 && overrides[0] != nil {
		if resolved, ok := overrides[0].ResolveDecay(key); ok {
			halfLife = resolved
		}
	}
	if halfLife <= 0 {
		return Evidence{}, ErrInvalid
	}
	current, err := currentFitness(set.Fitness, key, now)
	if err != nil {
		return Evidence{}, err
	}
	advisory, err := uniqueAdvisory(set.Advisory, key, now)
	if err != nil {
		return Evidence{}, err
	}
	validity, err := uniqueValidity(set.Validity, key, now)
	if err != nil {
		return Evidence{}, err
	}

	e := aggregateFitness(current, now, halfLife)
	e.Advisory = aggregateAdvisory(advisory, now, halfLife)
	e.Validity = aggregateValidity(validity, now, halfLife)
	return e, nil
}

func currentFitness(in []FitnessObservation, key Key, now time.Time) ([]FitnessObservation, error) {
	versions := make(map[string]FitnessObservation, len(in))
	byBase := make(map[string][]string)
	for _, raw := range in {
		o := raw
		o.Time = o.Time.UTC()
		if o.ID == "" || o.BaseID == "" || o.Key != key || !unit(o.Quality) || o.Latency < 0 || !nonnegative(o.Cost) || o.Time.IsZero() || o.Time.After(now) {
			return nil, ErrInvalid
		}
		if prior, ok := versions[o.ID]; ok {
			if !sameFitnessVersion(prior, o) {
				return nil, ErrInvalid
			}
			continue
		}
		versions[o.ID] = o
		byBase[o.BaseID] = append(byBase[o.BaseID], o.ID)
	}
	current := make([]FitnessObservation, 0, len(byBase))
	for baseID, ids := range byBase {
		base, ok := versions[baseID]
		if !ok || base.BaseID != baseID || base.Supersedes != "" {
			return nil, ErrInvalid
		}
		children := make(map[string]string, len(ids)-1)
		for _, id := range ids {
			o := versions[id]
			if id == baseID {
				continue
			}
			parent, ok := versions[o.Supersedes]
			if o.Supersedes == "" || !ok || parent.BaseID != baseID || !sameFitnessFact(base, o) {
				return nil, ErrInvalid
			}
			if _, fork := children[o.Supersedes]; fork {
				return nil, ErrInvalid
			}
			children[o.Supersedes] = id
		}
		head := base
		seen := 1
		for {
			next, ok := children[head.ID]
			if !ok {
				break
			}
			head = versions[next]
			seen++
		}
		if seen != len(ids) {
			return nil, ErrInvalid
		}
		current = append(current, head)
	}
	sort.Slice(current, func(i, j int) bool {
		return observationLess(current[i].Time, current[i].BaseID, current[j].Time, current[j].BaseID)
	})
	return current, nil
}

func uniqueAdvisory(in []AdvisoryObservation, key Key, now time.Time) ([]AdvisoryObservation, error) {
	seen := make(map[string]AdvisoryObservation, len(in))
	for _, raw := range in {
		o := raw
		o.Time = o.Time.UTC()
		if o.ID == "" || o.Key != key || !unit(o.Quality) || !unit(o.Confidence) || o.Time.IsZero() || o.Time.After(now) {
			return nil, ErrInvalid
		}
		if prior, ok := seen[o.ID]; ok {
			if prior != o {
				return nil, ErrInvalid
			}
			continue
		}
		seen[o.ID] = o
	}
	out := make([]AdvisoryObservation, 0, len(seen))
	for _, o := range seen {
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool { return observationLess(out[i].Time, out[i].ID, out[j].Time, out[j].ID) })
	return out, nil
}

func uniqueValidity(in []ValidityObservation, key Key, now time.Time) ([]ValidityObservation, error) {
	seen := make(map[string]ValidityObservation, len(in))
	for _, raw := range in {
		o := raw
		o.Time = o.Time.UTC()
		if o.ID == "" || o.Key != key || o.Time.IsZero() || o.Time.After(now) {
			return nil, ErrInvalid
		}
		if prior, ok := seen[o.ID]; ok {
			if prior != o {
				return nil, ErrInvalid
			}
			continue
		}
		seen[o.ID] = o
	}
	out := make([]ValidityObservation, 0, len(seen))
	for _, o := range seen {
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool { return observationLess(out[i].Time, out[i].ID, out[j].Time, out[j].ID) })
	return out, nil
}

func aggregateFitness(in []FitnessObservation, now time.Time, halfLife time.Duration) Evidence {
	e := Evidence{Samples: len(in), DecayApplied: true}
	var weight, quality, reliability, latency, cost, schemaWeight, compliance float64
	for i, o := range in {
		w := decayWeight(now, o.Time, halfLife)
		weight += w
		quality += w * o.Quality
		if o.Reliability {
			reliability += w
		}
		latency += w * float64(o.Latency)
		cost += w * o.Cost
		if o.Compliance != nil {
			schemaWeight += w
			if *o.Compliance {
				compliance += w
			}
		}
		if i == 0 {
			e.WindowStart = o.Time
		}
		e.WindowEnd, e.Updated = o.Time, o.Time
	}
	e.EffectiveSamples = weight
	if len(in) > 0 {
		e.DecayContribution = weight / float64(len(in))
	}
	if weight == 0 {
		e.Quality, e.Compliance, e.Reliability = .5, .5, .5
		return e
	}
	e.Quality, e.Reliability = quality/weight, reliability/weight
	e.Latency, e.Cost = time.Duration(latency/weight), cost/weight
	if schemaWeight == 0 {
		e.Compliance = .5
	} else {
		e.Compliance = compliance / schemaWeight
	}
	return e
}

func aggregateAdvisory(in []AdvisoryObservation, now time.Time, halfLife time.Duration) Advisory {
	a := Advisory{Samples: len(in), DecayApplied: true}
	var weight, qualityWeight, quality float64
	for i, o := range in {
		w := decayWeight(now, o.Time, halfLife)
		weight += w
		qualityWeight += w * o.Confidence
		quality += w * o.Confidence * o.Quality
		if i == 0 {
			a.WindowStart = o.Time
		}
		a.WindowEnd, a.Updated = o.Time, o.Time
	}
	a.EffectiveSamples = weight
	if len(in) > 0 {
		a.DecayContribution = weight / float64(len(in))
	}
	if weight > 0 {
		a.Confidence = qualityWeight / weight
	}
	if qualityWeight > 0 {
		a.Quality = quality / qualityWeight
	}
	return a
}

func aggregateValidity(in []ValidityObservation, now time.Time, halfLife time.Duration) Validity {
	v := Validity{Samples: len(in), DecayApplied: true}
	for i, o := range in {
		w := decayWeight(now, o.Time, halfLife)
		v.EffectiveSamples += w
		if o.Failed {
			v.Failures++
			v.EffectiveFailures += w
		}
		if i == 0 {
			v.WindowStart = o.Time
		}
		v.WindowEnd, v.Updated = o.Time, o.Time
	}
	if len(in) > 0 {
		v.DecayContribution = v.EffectiveSamples / float64(len(in))
	}
	return v
}

func decayWeight(now, observed time.Time, halfLife time.Duration) float64 {
	return math.Exp2(-float64(now.Sub(observed)) / float64(halfLife))
}

func observationLess(at time.Time, id string, bt time.Time, bid string) bool {
	if at.Equal(bt) {
		return id < bid
	}
	return at.Before(bt)
}

func sameFitnessVersion(a, b FitnessObservation) bool {
	return a.ID == b.ID && a.BaseID == b.BaseID && a.Supersedes == b.Supersedes && sameFitnessFact(a, b) && a.Quality == b.Quality
}

func sameFitnessFact(a, b FitnessObservation) bool {
	return a.BaseID == b.BaseID && a.Key == b.Key && sameBool(a.Compliance, b.Compliance) && a.Reliability == b.Reliability && a.Latency == b.Latency && a.Cost == b.Cost && a.Time.Equal(b.Time)
}

func sameBool(a, b *bool) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}
