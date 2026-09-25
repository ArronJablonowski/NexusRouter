package contextpolicy

import (
	"math"
	"testing"
)

func TestTiersPreserveAdvertisedCeiling(t *testing.T) {
	got := Tiers(96 * 1024)
	want := []int{32 * 1024, 64 * 1024, 96 * 1024}
	if len(got) != len(want) {
		t.Fatalf("tiers = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tiers = %v", got)
		}
	}
}

func TestSelectSmallestAccurateTierAndExplore(t *testing.T) {
	evidence := []Evidence{{ContextTokens: 32 * 1024, Samples: 4, Quality: .98}, {ContextTokens: 64 * 1024, Samples: 2, Quality: .985}}
	r := Request{AdvertisedMaximum: 128 * 1024, WorkingTier: 32 * 1024, EstimatedTokens: 20 * 1024, Evidence: evidence, MinimumSamples: 2}
	if got := Select(r); got != 32*1024 {
		t.Fatalf("selected %d", got)
	}
	r.Explore = true
	if got := Select(r); got != 128*1024 {
		t.Fatalf("exploration selected %d", got)
	}
}

func TestExplorationQualifiesHigherTierAfterMeasuredTierLoses(t *testing.T) {
	r := Request{AdvertisedMaximum: 128 * 1024, WorkingTier: 32 * 1024, EstimatedTokens: 20 * 1024, MinimumSamples: 3, Explore: true, Evidence: []Evidence{
		{ContextTokens: 32 * 1024, Samples: 6, Quality: .9},
		{ContextTokens: 64 * 1024, Samples: 3, Quality: .5},
	}}
	if got := Select(r); got != 128*1024 {
		t.Fatalf("repeated an already qualified, inferior allocation: %d", got)
	}
	r.Evidence = append(r.Evidence, Evidence{ContextTokens: 128 * 1024, Samples: 3, Quality: .6})
	if got := Select(r); got != 32*1024 {
		t.Fatalf("explored after the ladder was qualified: %d", got)
	}
}

func TestExplorationFinishesSparseTierBeforeMovingHigher(t *testing.T) {
	r := Request{AdvertisedMaximum: 128 * 1024, WorkingTier: 32 * 1024, MinimumSamples: 3, Explore: true, Evidence: []Evidence{
		{ContextTokens: 32 * 1024, Samples: 6, Quality: .9},
		{ContextTokens: 64 * 1024, Samples: 2, Quality: .5},
	}}
	if got := Select(r); got != 64*1024 {
		t.Fatalf("skipped an incompletely measured allocation: %d", got)
	}
}

func TestExplorationBeyondQualifiedTierStillHonorsResourceLimits(t *testing.T) {
	for _, limit := range []string{"memory", "timeout", "swap"} {
		t.Run(limit, func(t *testing.T) {
			r := Request{AdvertisedMaximum: 128 * 1024, WorkingTier: 32 * 1024, MinimumSamples: 3, Explore: true, Evidence: []Evidence{
				{ContextTokens: 32 * 1024, Samples: 6, Quality: .9},
				{ContextTokens: 64 * 1024, Samples: 3, Quality: .5},
			}}
			switch limit {
			case "memory":
				r.FitsMemory = func(tier int) bool { return tier < 128*1024 }
			case "timeout":
				r.Evidence = append(r.Evidence, Evidence{ContextTokens: 128 * 1024, Timeouts: 1})
			case "swap":
				r.Evidence = append(r.Evidence, Evidence{ContextTokens: 128 * 1024, MaxSwapGrowth: MaxSwapGrowth + 1})
			}
			if got := Select(r); got != 32*1024 {
				t.Fatalf("resource limit allowed exploration at %d", got)
			}
		})
	}
}

func TestSelectBacksOffAfterFaultOrSwapGrowth(t *testing.T) {
	for _, evidence := range [][]Evidence{
		{{ContextTokens: 64 * 1024, Timeouts: 1}},
		{{ContextTokens: 64 * 1024, ProviderErrors: 1}},
		{{ContextTokens: 64 * 1024, MaxSwapGrowth: MaxSwapGrowth + 1}},
	} {
		r := Request{AdvertisedMaximum: 128 * 1024, WorkingTier: 32 * 1024, EstimatedTokens: 48 * 1024, Evidence: evidence}
		if got := Select(r); got != 32*1024 {
			t.Fatalf("selected %d with evidence %+v", got, evidence)
		}
	}
}

func TestSelectHonorsMemoryAvailability(t *testing.T) {
	r := Request{AdvertisedMaximum: 128 * 1024, WorkingTier: 32 * 1024, EstimatedTokens: 40 * 1024, MemoryAvailable: 70, EstimatedMemory: func(tokens int) uint64 { return uint64(tokens / 1024) }}
	if got := Select(r); got != 64*1024 {
		t.Fatalf("selected %d", got)
	}
}

func TestFaultDoesNotPoisonHealthyLowerTier(t *testing.T) {
	r := Request{AdvertisedMaximum: 262144, WorkingTier: 32768, EstimatedTokens: 40000, Evidence: []Evidence{{ContextTokens: 131072, Timeouts: 1}}}
	if got := Select(r); got != 65536 {
		t.Fatalf("healthy 64K lost: %d", got)
	}
	r.Evidence = []Evidence{{ContextTokens: 32768, ProviderErrors: 1}}
	if got := Select(r); got != 0 {
		t.Fatalf("unsafe tier returned: %d", got)
	}
}

func TestSparseLargerTierCannotDisplaceProvenTier(t *testing.T) {
	r := Request{AdvertisedMaximum: 131072, WorkingTier: 32768, MinimumSamples: 5, Evidence: []Evidence{{ContextTokens: 32768, Samples: 10, Quality: .8}, {ContextTokens: 65536, Samples: 1, Quality: 1}}}
	if got := Select(r); got != 32768 {
		t.Fatalf("single lucky sample promoted tier: %d", got)
	}
	r.Explore = true
	r.FitsMemory = func(tier int) bool { return tier <= 32768 }
	if got := Select(r); got != 32768 {
		t.Fatalf("explored without memory: %d", got)
	}
	r.FitsMemory = func(int) bool { return false }
	if got := Select(r); got != 0 {
		t.Fatalf("returned inadmissible fallback: %d", got)
	}
}

func TestSelectUsesLearnedNoncanonicalTier(t *testing.T) {
	r := Request{AdvertisedMaximum: 131072, WorkingTier: 32768, MinimumSamples: 2, Evidence: []Evidence{
		{ContextTokens: 32768, Samples: 4, Quality: .5},
		{ContextTokens: 49152, Samples: 4, Quality: 1},
	}}
	if got := Select(r); got != 49152 {
		t.Fatalf("discarded accurate measured tier: %d", got)
	}
}

func TestSelectIgnoresInvalidAccuracyButRetainsSafetyEvidence(t *testing.T) {
	for _, quality := range []float64{math.Inf(1), math.NaN(), 1.1, -1} {
		r := Request{AdvertisedMaximum: 131072, WorkingTier: 32768, MinimumSamples: 2, Evidence: []Evidence{
			{ContextTokens: 16384, Samples: 4, Quality: quality},
			{ContextTokens: 32768, Samples: 4, Quality: .8},
		}}
		if got := Select(r); got != 32768 {
			t.Fatalf("invalid accuracy %v selected tier %d", quality, got)
		}
		r.Evidence[0].Timeouts = 1
		if got := Select(r); got != 0 {
			t.Fatalf("invalid accuracy hid a real allocation fault: %d", got)
		}
	}
}
