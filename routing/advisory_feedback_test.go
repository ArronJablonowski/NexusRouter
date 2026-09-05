package routing

import (
	"math"
	"testing"
)

func TestSparseDirectFeedbackDominatesContradictoryAudits(t *testing.T) {
	for _, domain := range []string{"creative", "code", "unknown"} {
		for _, direct := range []float64{0, .5, 1} {
			for _, audit := range []float64{0, 1} {
				r, c, now := fixture()
				r.Domain = domain
				p := Defaults()
				p.Exploration = 0
				// Isolate quality in the total score while preserving default
				// sample shrinkage. One direct sample represents one user vote.
				p.Weights = Weights{Quality: 1}
				key := Key{c[0].Model, c[0].Provider, domain, r.Profile}
				e := Evidence{Updated: now, Samples: 1, Quality: direct, Advisory: Advisory{Samples: 100, Quality: audit, Confidence: 1, Updated: now}}
				out, err := Select(r, p, c[:1], map[Key]Evidence{key: e}, now, .5)
				if err != nil {
					t.Fatal(err)
				}
				got := out.Primary
				directQuality := .5 + (direct-.5)/float64(p.MinSamples)
				if direct == .5 {
					if got.Score != .5 || got.AdvisoryInfluence != 0 {
						t.Fatalf("neutral direct evidence moved by reviews: %+v", got)
					}
				} else if direct != audit {
					want := .5 + (directQuality-.5)/2
					if math.Abs(got.Score-want) > 1e-12 {
						t.Fatalf("domain=%s direct=%g audit=%g score=%g want=%g", domain, direct, audit, got.Score, want)
					}
					if (got.Score-.5)*(directQuality-.5) <= 0 {
						t.Fatal("audits reversed direct feedback")
					}
				} else if math.Abs(got.Score-.5) < math.Abs(directQuality-.5) {
					t.Fatal("agreeing advisory weakened feedback")
				}
				if got.Samples != 1 || got.Confidence != 1/float64(p.MinSamples) || got.AdvisorySamples != 100 {
					t.Fatalf("advisory fabricated direct evidence: %+v", got)
				}
				// The exposed influence must describe the actual applied delta.
				if math.Abs(got.Score-(directQuality+got.AdvisoryInfluence*(audit-.5))) > 1e-12 {
					t.Fatal("reported influence differs from score")
				}
			}
		}
	}
}

func TestDirectFeedbackAdvisoryBoundPrecedesObjectiveValidity(t *testing.T) {
	r, c, now := fixture()
	p := Defaults()
	p.Exploration = 0
	p.Weights = Weights{Quality: 1}
	e := Evidence{Updated: now, Samples: 1, Quality: 1, Advisory: Advisory{Samples: 100, Quality: 0, Confidence: 1, Updated: now}, Validity: Validity{Samples: 100, Failures: 100, Updated: now}}
	out, err := Select(r, p, c[:1], map[Key]Evidence{{c[0].Model, c[0].Provider, r.Domain, r.Profile}: e}, now, .5)
	if err != nil || out.Primary.Score != 0 || out.Primary.ValidityPenalty != 1 {
		t.Fatalf("objective invalidity was bypassed: %+v %v", out, err)
	}
}
