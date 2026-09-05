package routing

import (
	"math"
	"testing"
)

func TestValidityDiscountIsSeparateFromSubjectiveQuality(t *testing.T) {
	r, c, now := fixture()
	p := Defaults()
	p.Exploration = 0
	key := Key{"cloud", "remote", r.Domain, r.Profile}
	e := Evidence{Updated: now, Samples: 100, Quality: 1, Compliance: 1, Reliability: 1}
	selectRank := func(e Evidence) Ranked {
		t.Helper()
		out, err := Select(r, p, c, map[Key]Evidence{key: e}, now, .5)
		if err != nil {
			t.Fatal(err)
		}
		for _, rank := range out.Ranked {
			if rank.Model == key.Model {
				return rank
			}
		}
		t.Fatal("missing rank")
		return Ranked{}
	}
	base := selectRank(e)
	e.Validity = Validity{Samples: 100, Updated: now}
	passed := selectRank(e)
	if passed.Score != base.Score || passed.Samples != base.Samples {
		t.Fatal("validity pass invented quality or measurements")
	}
	e.Validity.Failures = 100
	failed := selectRank(e)
	if math.Abs(base.Score-failed.Score-p.Weights.Quality) > 1e-9 || failed.ValidityPenalty != 1 || failed.Confidence != base.Confidence {
		t.Fatalf("%+v %+v", base, failed)
	}
	e.Validity.Updated = now.Add(-p.HalfLife)
	if rank := selectRank(e); rank.ValidityPenalty != .5 {
		t.Fatal(rank)
	}
	e.Validity = Validity{Samples: 1, Failures: 1, Updated: now}
	if rank := selectRank(e); rank.ValidityPenalty != 1/float64(p.MinSamples) {
		t.Fatal(rank)
	}
}

func TestValidityRejectsInvalidCountsAndTime(t *testing.T) {
	r, c, now := fixture()
	for _, v := range []Validity{{Samples: -1}, {Failures: 1}, {Samples: 1, Failures: 2, Updated: now}, {Samples: 1}, {Samples: 1, Updated: now.Add(1)}} {
		_, err := Select(r, Defaults(), c, map[Key]Evidence{{"cloud", "remote", r.Domain, r.Profile}: {Updated: now, Validity: v}}, now, .5)
		if err == nil {
			t.Fatal("invalid validity accepted", v)
		}
	}
}
