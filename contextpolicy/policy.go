// Package contextpolicy selects bounded model context windows from learned tiers.
package contextpolicy

import "sort"

const (
	ProvenTier       = 32 * 1024
	MaxSwapGrowth    = uint64(5 << 30)
	qualityTolerance = 0.01
)

// Evidence is the durable outcome summary for one model and context tier.
type Evidence struct {
	ContextTokens  int
	Samples        int
	Quality        float64
	LatencyMillis  float64
	Timeouts       int
	ProviderErrors int
	PeakMemory     uint64
	MaxSwapGrowth  uint64
}

// Request contains the facts available before resource admission.
type Request struct {
	AdvertisedMaximum int
	WorkingTier       int
	EstimatedTokens   int
	MemoryAvailable   uint64
	EstimatedMemory   func(contextTokens int) uint64
	Evidence          []Evidence
	Explore           bool
	MinimumSamples    int
}

// Tiers returns the canonical exploration ladder capped by the advertised
// model capability. The advertised value remains a ceiling, not a default.
func Tiers(advertised int) []int {
	if advertised < 1 {
		return nil
	}
	values := []int{ProvenTier, 64 * 1024, 128 * 1024, advertised}
	seen := map[int]bool{}
	out := make([]int, 0, len(values))
	for _, value := range values {
		value = min(value, advertised)
		if value > 0 && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Ints(out)
	return out
}

// Select chooses the smallest safe tier that fits the task. A larger tier is
// explored only when requested and the next tier has no recorded hard fault.
func Select(r Request) int {
	tiers := Tiers(r.AdvertisedMaximum)
	if len(tiers) == 0 {
		return 0
	}
	working := r.WorkingTier
	if working < 1 {
		working = min(ProvenTier, r.AdvertisedMaximum)
	}
	required := max(r.EstimatedTokens, 1)
	evidence := make(map[int]Evidence, len(r.Evidence))
	for _, item := range r.Evidence {
		evidence[item.ContextTokens] = item
	}
	for _, item := range r.Evidence {
		if item.ContextTokens >= working && (item.Timeouts > 0 || item.ProviderErrors > 0 || item.MaxSwapGrowth > MaxSwapGrowth) {
			return min(working, r.AdvertisedMaximum)
		}
	}
	safe := func(tier int) bool {
		item := evidence[tier]
		if item.Timeouts > 0 || item.ProviderErrors > 0 || item.MaxSwapGrowth > MaxSwapGrowth {
			return false
		}
		if r.EstimatedMemory != nil && r.MemoryAvailable > 0 && r.EstimatedMemory(tier) > r.MemoryAvailable {
			return false
		}
		return true
	}
	selected := 0
	for _, tier := range tiers {
		if tier >= required && safe(tier) {
			selected = tier
			break
		}
	}
	if selected == 0 {
		// Back off to the largest lower safe tier; runtime compaction can then
		// reduce the request, while an unsafe larger allocation is never made.
		for i := len(tiers) - 1; i >= 0; i-- {
			if tiers[i] <= working && safe(tiers[i]) {
				return tiers[i]
			}
		}
		return min(working, r.AdvertisedMaximum)
	}

	// Once multiple tiers have evidence, retain the smallest tier whose quality
	// preserves the best observed accuracy within one percentage point.
	bestQuality := -1.0
	for _, item := range r.Evidence {
		if item.Samples > 0 && item.Timeouts == 0 && item.ProviderErrors == 0 && item.MaxSwapGrowth <= MaxSwapGrowth && item.Quality > bestQuality {
			bestQuality = item.Quality
		}
	}
	if bestQuality >= 0 {
		for _, tier := range tiers {
			item := evidence[tier]
			if tier >= required && safe(tier) && item.Samples > 0 && item.Quality+qualityTolerance >= bestQuality {
				selected = tier
				break
			}
		}
	}
	minimumSamples := max(r.MinimumSamples, 1)
	if r.Explore && evidence[selected].Samples >= minimumSamples {
		for _, tier := range tiers {
			if tier > selected && safe(tier) {
				return tier
			}
		}
	}
	return selected
}
