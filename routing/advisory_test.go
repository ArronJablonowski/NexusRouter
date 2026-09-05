package routing

import (
	"testing"
)

func TestAdvisoryQualityIsBoundedAndDoesNotCreateExecutionSamples(t *testing.T) {
	r, c, now := fixture()
	p := Defaults()
	p.Exploration = 0
	key := Key{"cloud", "remote", r.Domain, r.Profile}
	e := Evidence{Updated: now, Advisory: Advisory{Samples: 100, Quality: 1, Confidence: 1, Updated: now}}
	out, err := Select(r, p, c, map[Key]Evidence{key: e}, now, .5)
	if err != nil {
		t.Fatal(err)
	}
	if out.Primary.Model != "cloud" || out.Primary.Samples != 0 || out.Primary.Confidence != 0 || out.Primary.AdvisoryInfluence != .25 {
		t.Fatalf("%+v", out)
	}
	r.Domain = "creative"
	key.Domain = r.Domain
	out, err = Select(r, p, c, map[Key]Evidence{key: e}, now, .5)
	if err != nil || out.Primary.AdvisoryInfluence != .1 {
		t.Fatalf("%+v %v", out, err)
	}
	e.Samples = 100
	e.Quality = 0
	e.Compliance = .5
	e.Reliability = .5
	out, err = Select(r, p, c, map[Key]Evidence{key: e}, now, .5)
	if err != nil {
		t.Fatal(err)
	}
	for _, rank := range out.Ranked {
		if rank.Model == "cloud" && rank.AdvisoryInfluence != 0 {
			t.Fatal("judge outweighed confident direct evidence")
		}
	}
}

func TestStaleAndUncertainAuditsLoseInfluence(t *testing.T) {
	r, c, now := fixture()
	p := Defaults()
	p.Exploration = 0
	e := Evidence{Updated: now, Advisory: Advisory{Samples: 1, Quality: 1, Confidence: .5, Updated: now.Add(-p.HalfLife)}}
	out, err := Select(r, p, c, map[Key]Evidence{{"cloud", "remote", r.Domain, r.Profile}: e}, now, .5)
	if err != nil {
		t.Fatal(err)
	}
	if out.Primary.AdvisoryInfluence <= 0 || out.Primary.AdvisoryInfluence >= .01 {
		t.Fatalf("%+v", out.Primary)
	}
}
