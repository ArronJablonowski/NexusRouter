package routing

import (
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var explanationReasons = map[string]int{
	"mode": 0, "privacy": 1, "health": 2, "policy": 3,
	"credential": 4, "capacity": 5, "context": 6, "budget": 7,
	"capability": 8,
}

// ValidateExplanation verifies the bounded structural invariants of a stored
// routing decision. It cannot recompute historical health, capacity or fitness.
func ValidateExplanation(candidates []Candidate, policy *Policy, selection *Selection) error {
	if policy == nil || selection == nil || len(candidates) < 1 || len(candidates) > 256 || !validExplanationPolicy(*policy) {
		return ErrInvalid
	}
	candidateByRoute := make(map[[2]string]Candidate, len(candidates))
	for _, candidate := range candidates {
		key := [2]string{candidate.Model, candidate.Provider}
		if !validExplanationCandidate(candidate) {
			return ErrInvalid
		}
		if _, exists := candidateByRoute[key]; exists {
			return ErrInvalid
		}
		candidateByRoute[key] = candidate
	}
	if len(selection.Ranked) < 1 || len(selection.Ranked) > len(candidates) || len(selection.Fallbacks) != len(selection.Ranked)-1 || len(selection.Excluded)+len(selection.Ranked) != len(candidates) {
		return ErrInvalid
	}
	ranked := make(map[[2]string]Ranked, len(selection.Ranked))
	primary := false
	for i, item := range selection.Ranked {
		key := [2]string{item.Model, item.Provider}
		candidate, exists := candidateByRoute[key]
		if !exists || !validExplanationRanked(item) || !validExplanationPolicyProjection(item, *policy) || item.FailureDomain != candidate.FailureDomain {
			return ErrInvalid
		}
		if _, exists := ranked[key]; exists {
			return ErrInvalid
		}
		if i > 0 && rankedExplanationLess(item, selection.Ranked[i-1]) {
			return ErrInvalid
		}
		ranked[key] = item
		primary = primary || item == selection.Primary
	}
	if !primary || !validExplanationRanked(selection.Primary) || (!selection.Explored && selection.Primary != selection.Ranked[0]) || (selection.Explored && (len(selection.Ranked) < 2 || selection.Primary == selection.Ranked[0])) {
		return ErrInvalid
	}
	expectedFallbacks := explanationFallbacks(selection.Ranked, selection.Primary)
	for i, item := range selection.Fallbacks {
		key := [2]string{item.Model, item.Provider}
		stored, exists := ranked[key]
		if !exists || stored != item || item != expectedFallbacks[i] {
			return ErrInvalid
		}
	}
	excluded := map[[2]string]bool{}
	for _, item := range selection.Excluded {
		key := [2]string{item.Model, item.Provider}
		_, candidateExists := candidateByRoute[key]
		_, rankedExists := ranked[key]
		if !candidateExists || rankedExists || excluded[key] || len(item.Reasons) < 1 || len(item.Reasons) > len(explanationReasons) {
			return ErrInvalid
		}
		seenReasons := map[string]bool{}
		previous := -1
		for _, reason := range item.Reasons {
			order, known := explanationReasons[reason]
			if !known || seenReasons[reason] || order <= previous {
				return ErrInvalid
			}
			seenReasons[reason] = true
			previous = order
		}
		excluded[key] = true
	}
	return nil
}

func explanationFallbacks(ranked []Ranked, primary Ranked) []Ranked {
	out := make([]Ranked, 0, len(ranked)-1)
	usedDomains := map[string]bool{}
	if primary.FailureDomain != "" {
		usedDomains[primary.FailureDomain] = true
	}
	added := make(map[[2]string]bool, len(ranked))
	for _, item := range ranked {
		if item == primary || item.FailureDomain == "" || usedDomains[item.FailureDomain] {
			continue
		}
		out = append(out, item)
		added[[2]string{item.Model, item.Provider}] = true
		usedDomains[item.FailureDomain] = true
	}
	for _, item := range ranked {
		if item != primary && !added[[2]string{item.Model, item.Provider}] {
			out = append(out, item)
		}
	}
	return out
}

func validExplanationPolicy(p Policy) bool {
	if p.MinSamples < 1 || p.HalfLife <= 0 || p.LatencyScale <= 0 || !nonnegative(p.CostScale) || p.CostScale == 0 || !unit(p.Exploration) || p.Exploration > .25 {
		return false
	}
	sum := 0.0
	for _, weight := range []float64{p.Weights.Quality, p.Weights.Compliance, p.Weights.Reliability, p.Weights.Latency, p.Weights.Cost, p.Weights.Recency, p.Weights.Uncertainty} {
		if !unit(weight) {
			return false
		}
		sum += weight
	}
	return math.Abs(sum-1) <= 1e-9
}

func validExplanationCandidate(c Candidate) bool {
	if !safeExplanationLabel(c.Model, 512) || !safeExplanationLabel(c.Provider, 128) || c.ContextTokens < 1 || !nonnegative(c.EstimatedCost) || !safeOptionalExplanationLabel(c.FailureDomain, 128) || len(c.Capabilities) < 1 || len(c.Capabilities) > 128 {
		return false
	}
	seen := map[string]bool{}
	for _, capability := range c.Capabilities {
		if !safeExplanationLabel(capability, 128) || seen[capability] {
			return false
		}
		seen[capability] = true
	}
	return true
}

func validExplanationRanked(r Ranked) bool {
	return safeExplanationLabel(r.Model, 512) && safeExplanationLabel(r.Provider, 128) && safeOptionalExplanationLabel(r.FailureDomain, 128) && r.Samples >= 0 && r.AdvisorySamples >= 0 && r.ValiditySamples >= 0 && r.ValidityFailures >= 0 && r.ValidityFailures <= r.ValiditySamples && unit(r.Score) && unit(r.Confidence) && unit(r.Recency) && unit(r.Uncertainty) && unit(r.AdvisoryInfluence) && unit(r.ValidityPenalty) && math.Abs(r.Confidence+r.Uncertainty-1) <= 1e-9 && validExplanationDecay(r.DecayApplied, r.Samples, r.EffectiveSamples, r.DecayContribution, r.WindowStart, r.WindowEnd) && validExplanationDecay(r.AdvisoryDecayApplied, r.AdvisorySamples, r.AdvisoryEffectiveSamples, r.AdvisoryDecayContribution, r.AdvisoryWindowStart, r.AdvisoryWindowEnd) && validExplanationDecay(r.ValidityDecayApplied, r.ValiditySamples, r.ValidityEffectiveSamples, r.ValidityDecayContribution, r.ValidityWindowStart, r.ValidityWindowEnd) && (!r.ValidityDecayApplied && r.ValidityEffectiveFailures == 0 || r.ValidityDecayApplied && nonnegative(r.ValidityEffectiveFailures) && r.ValidityEffectiveFailures <= r.ValidityEffectiveSamples)
}

func validExplanationDecay(applied bool, samples int, effective, contribution float64, start, end time.Time) bool {
	if !applied {
		return effective == 0 && contribution == 0 && start.IsZero() && end.IsZero()
	}
	if samples < 0 || !nonnegative(effective) || effective > float64(samples) || !unit(contribution) {
		return false
	}
	if samples == 0 {
		return effective == 0 && contribution == 0 && start.IsZero() && end.IsZero()
	}
	return !start.IsZero() && !end.IsZero() && !start.After(end) && math.Abs(contribution-effective/float64(samples)) <= 1e-12
}

func validExplanationPolicyProjection(r Ranked, p Policy) bool {
	if r.DecayApplied {
		confidence := math.Min(1, r.EffectiveSamples/float64(p.MinSamples))
		if math.Abs(r.Confidence-confidence) > 1e-12 || math.Abs(r.Recency-r.DecayContribution) > 1e-12 {
			return false
		}
	}
	if r.ValidityDecayApplied {
		penalty := 0.0
		if r.ValidityEffectiveSamples > 0 {
			penalty = r.ValidityEffectiveFailures / r.ValidityEffectiveSamples * math.Min(1, r.ValidityEffectiveSamples/float64(p.MinSamples))
		}
		if math.Abs(r.ValidityPenalty-penalty) > 1e-12 {
			return false
		}
	}
	return true
}

func rankedExplanationLess(a, b Ranked) bool {
	if a.Score != b.Score {
		return a.Score > b.Score
	}
	if a.Model != b.Model {
		return a.Model < b.Model
	}
	return a.Provider < b.Provider
}

func safeOptionalExplanationLabel(value string, limit int) bool {
	return value == "" || safeExplanationLabel(value, limit)
}

func safeExplanationLabel(value string, limit int) bool {
	return value != "" && len(value) <= limit && utf8.ValidString(value) && strings.TrimSpace(value) == value && !strings.ContainsFunc(value, unicode.IsControl)
}
