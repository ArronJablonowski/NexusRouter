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
