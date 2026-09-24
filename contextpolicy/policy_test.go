package contextpolicy

import "testing"

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
	if got := Select(r); got != 64*1024 {
		t.Fatalf("exploration selected %d", got)
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
