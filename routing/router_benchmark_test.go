package routing

import (
	"fmt"
	"testing"
	"time"
)

// These benchmarks isolate the pure selector: no configuration parsing, resource
// sampling, database access, classification, or provider inference is included.
// Fixtures are built and qualified outside the measured loop.
func BenchmarkSelectEvidence(b *testing.B) {
	for _, count := range []int{2, 8, 64} {
		b.Run(fmt.Sprintf("models_%d", count), func(b *testing.B) {
			r, p, candidates, evidence, now := routerBenchmarkFixture(count)
			benchmarkQualifiedSelection(b, r, p, candidates, evidence, now, .9, count, 0, false)
		})
	}
}

func BenchmarkSelectConstrainedFallback(b *testing.B) {
	r, p, candidates, evidence, now := routerBenchmarkFixture(64)
	r.Mode, r.LocalRequired = "local_only", true
	// Eight viable local candidates retain distinct failure domains. Others
	// fail one or more hard constraints; this is not an all-rejected fast path.
	for i := range candidates {
		candidates[i].Local = true
		if i < 8 {
			continue
		}
		switch i % 7 {
		case 0:
			candidates[i].Local = false
		case 1:
			candidates[i].Healthy = false
		case 2:
			candidates[i].PolicyAllowed = false
		case 3:
			candidates[i].CapacityAvailable = false
		case 4:
			candidates[i].ContextTokens = 512
		case 5:
			candidates[i].EstimatedCost = 1
		case 6:
			candidates[i].Capabilities = []string{"chat"}
		}
	}
	benchmarkQualifiedSelection(b, r, p, candidates, evidence, now, .9, 8, 56, false)
}

func BenchmarkSelectExploration(b *testing.B) {
	r, p, candidates, evidence, now := routerBenchmarkFixture(8)
	r.AllowExploration = true
	// Half the models have no measured history. Bounded exploration still
	// ranks all eligible candidates and builds the complete fallback list.
	for i := 4; i < len(candidates); i++ {
		delete(evidence, Key{candidates[i].Model, candidates[i].Provider, r.Domain, r.Profile})
	}
	benchmarkQualifiedSelection(b, r, p, candidates, evidence, now, p.Exploration*.75, 8, 0, true)
}

func routerBenchmarkFixture(count int) (Request, Policy, []Candidate, map[Key]Evidence, time.Time) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	r := Request{Mode: "hybrid", Domain: "coding", Profile: "default", Capabilities: []string{"code", "tools"}, ContextTokens: 8192, MaxCost: .05}
	candidates := make([]Candidate, count)
	evidence := make(map[Key]Evidence, count)
	for i := range candidates {
		c := Candidate{Model: fmt.Sprintf("model-%03d", i), Provider: fmt.Sprintf("provider-%d", i%4), FailureDomain: fmt.Sprintf("host-%d", i%2), Local: i%2 == 0, Capabilities: []string{"chat", "code", "tools", "json"}, ContextTokens: 32768, Healthy: true, PolicyAllowed: true, CapacityAvailable: true, EstimatedCost: .002 * float64(i%4)}
		candidates[i] = c
		evidence[Key{c.Model, c.Provider, r.Domain, r.Profile}] = Evidence{Samples: 30 + i%21, Quality: .7 + .02*float64(i%10), Compliance: .95, Reliability: .98, Latency: time.Duration(1+i%8) * time.Second, Cost: c.EstimatedCost, Updated: now.Add(-time.Duration(1+i%30) * 24 * time.Hour), Advisory: Advisory{Samples: 12, Quality: .8, Confidence: .75, Updated: now.Add(-24 * time.Hour)}, Validity: Validity{Samples: 40, Failures: i % 3, Updated: now.Add(-12 * time.Hour)}}
	}
	return r, Defaults(), candidates, evidence, now
}

var routerBenchmarkSelection Selection

func benchmarkQualifiedSelection(b *testing.B, r Request, p Policy, candidates []Candidate, evidence map[Key]Evidence, now time.Time, draw float64, eligible, excluded int, explored bool) {
	b.Helper()
	got, err := Select(r, p, candidates, evidence, now, draw)
	if err != nil || len(got.Ranked) != eligible || len(got.Excluded) != excluded || len(got.Fallbacks) != eligible-1 || got.Primary.Model == "" || got.Explored != explored {
		b.Fatalf("invalid benchmark fixture: ranked=%d excluded=%d fallback=%d explored=%v err=%v", len(got.Ranked), len(got.Excluded), len(got.Fallbacks), got.Explored, err)
	}
	if got.Fallbacks[0].FailureDomain == got.Primary.FailureDomain {
		b.Fatal("fixture lacks diverse first fallback")
	}
	if !explored && (got.Primary.Samples == 0 || got.Primary.Confidence <= 0 || got.Primary.AdvisorySamples == 0 || got.Primary.ValiditySamples == 0) {
		b.Fatal("fixture did not score measured evidence")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		got, err = Select(r, p, candidates, evidence, now, draw)
		if err != nil {
			b.Fatal(err)
		}
	}
	routerBenchmarkSelection = got
}
