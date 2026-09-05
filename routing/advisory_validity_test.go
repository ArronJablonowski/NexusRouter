package routing

import "testing"

// Strong review approval must not restore quality credit removed by objective
// invalidity, even when there are no direct fitness samples to suppress reviews.
func TestAdvisoryCannotEraseObjectiveInvalidity(t *testing.T) {
	for _, domain := range []string{"code", "creative"} {
		t.Run(domain, func(t *testing.T) {
			r, candidates, now := fixture()
			r.Domain = domain
			p := Defaults()
			p.Exploration = 0
			key := Key{"cloud", "remote", domain, r.Profile}
			e := Evidence{Updated: now, Advisory: Advisory{Samples: 100, Quality: 1, Confidence: 1, Updated: now}, Validity: Validity{Samples: 100, Failures: 100, Updated: now}}
			rank := func(e Evidence) Ranked {
				t.Helper()
				out, err := Select(r, p, candidates, map[Key]Evidence{key: e}, now, .5)
				if err != nil {
					t.Fatal(err)
				}
				for _, candidate := range out.Ranked {
					if candidate.Model == key.Model {
						return candidate
					}
				}
				t.Fatal("candidate disappeared instead of retaining evidence")
				return Ranked{}
			}
			approved := rank(e)
			if approved.AdvisoryInfluence <= 0 || approved.ValidityPenalty != 1 || approved.Samples != 0 || approved.Confidence != 0 {
				t.Fatalf("missing separate advisory and objective evidence: %+v", approved)
			}
			e.Advisory.Quality = 0
			rejected := rank(e)
			if approved.Score != rejected.Score {
				t.Fatalf("judge opinion restored objectively invalid quality: approved=%+v rejected=%+v", approved, rejected)
			}
			e.Validity.Failures = 0
			valid := rank(e)
			if valid.Score <= rejected.Score || valid.Samples != rejected.Samples || valid.Confidence != rejected.Confidence {
				t.Fatal("objective validity confused with execution or review evidence", valid, rejected)
			}
		})
	}
}
