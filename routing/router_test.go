package routing

import (
	"errors"
	"math"
	"reflect"
	"testing"
	"time"
)

func fixture() (Request, []Candidate, time.Time) {
	return Request{Mode: "hybrid", Domain: "code", Profile: "default", Capabilities: []string{"code"}, ContextTokens: 100, MaxCost: 1}, []Candidate{{Model: "local", Provider: "ollama", Local: true, FailureDomain: "host", Capabilities: []string{"code"}, ContextTokens: 1000, Healthy: true, PolicyAllowed: true, CapacityAvailable: true}, {Model: "cloud", Provider: "remote", FailureDomain: "cloud-a", Capabilities: []string{"code"}, ContextTokens: 1000, Healthy: true, PolicyAllowed: true, CapacityAvailable: true, EstimatedCost: .1}}, time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)
}

func TestHardConstraintsBeforeExploration(t *testing.T) {
	for _, name := range []string{"privacy", "mode", "health", "policy", "credential", "capacity", "context", "budget", "capability"} {
		t.Run(name, func(t *testing.T) {
			r, c, now := fixture()
			switch name {
			case "privacy":
				r.LocalRequired = true
			case "mode":
				r.Mode = "local_only"
			case "health":
				c[1].Healthy = false
			case "policy":
				c[1].PolicyAllowed = false
			case "credential":
				c[1].CredentialRequired = true
			case "capacity":
				c[1].CapacityAvailable = false
			case "context":
				c[1].ContextTokens = 1
			case "budget":
				c[1].EstimatedCost = 2
			case "capability":
				c[1].Capabilities = nil
			}
			e := map[Key]Evidence{{"cloud", "remote", "code", "default"}: {Samples: 100, Quality: 1, Compliance: 1, Reliability: 1, Updated: now}}
			out, err := Select(r, Defaults(), c, e, now, 0)
			if err != nil || out.Primary.Model != "local" || len(out.Fallbacks) != 0 || len(out.Excluded) != 1 {
				t.Fatalf("%+v %v", out, err)
			}
		})
	}
}

func TestRankingDecayAndDomainIsolation(t *testing.T) {
	r, c, now := fixture()
	p := Defaults()
	e := map[Key]Evidence{{"local", "ollama", "code", "default"}: {Samples: 100, Quality: 1, Compliance: 1, Reliability: 1, Updated: now}}
	out, err := Select(r, p, c, e, now, .9)
	if err != nil || out.Primary.Model != "local" || out.Primary.Confidence != 1 || out.Primary.Recency != 1 || out.Primary.Uncertainty != 0 {
		t.Fatalf("%+v %v", out, err)
	}
	e[Key{"local", "ollama", "code", "default"}] = Evidence{Samples: 100, Quality: 1, Compliance: 1, Reliability: 1, Updated: now.Add(-p.HalfLife)}
	decayed, err := Select(r, p, c, e, now, .9)
	if err != nil || decayed.Primary.Confidence != .5 || decayed.Primary.Recency != .5 || decayed.Primary.Uncertainty != .5 || decayed.Primary.Score >= out.Primary.Score {
		t.Fatalf("%+v %v", decayed, err)
	}
	r.Domain = "math"
	isolated, err := Select(r, p, c, e, now, .9)
	if err != nil || isolated.Primary.Confidence != 0 || isolated.Primary.Recency != 0 || isolated.Primary.Uncertainty != 1 {
		t.Fatalf("%+v %v", isolated, err)
	}
	if isolated.Ranked[0].Score != isolated.Ranked[1].Score {
		t.Fatal("unrelated domain evidence leaked")
	}
}

func TestExplorationAndStableOrdering(t *testing.T) {
	r, c, now := fixture()
	r.AllowExploration = true
	p := Defaults()
	a, err := Select(r, p, c, nil, now, .9)
	if err != nil {
		t.Fatal(err)
	}
	c[0], c[1] = c[1], c[0]
	b, err := Select(r, p, c, nil, now, .9)
	if err != nil || !reflect.DeepEqual(a, b) {
		t.Fatal("ordering depends on input order")
	}
	explored, err := Select(r, p, c, nil, now, p.Exploration*.75)
	if err != nil || !explored.Explored || explored.Primary.Model != a.Ranked[1].Model {
		t.Fatalf("%+v %v", explored, err)
	}
	boundary, err := Select(r, p, c, nil, now, p.Exploration)
	if err != nil || boundary.Explored {
		t.Fatal("exploration exceeded configured fraction")
	}
}

func TestExplorationAlwaysSelectsAnEligibleAlternative(t *testing.T) {
	r, candidates, now := fixture()
	r.AllowExploration = true
	third := candidates[0]
	third.Model = "new-local"
	candidates = append(candidates, third)
	p := Defaults()
	evidence := map[Key]Evidence{
		{"local", "ollama", "code", "default"}: {Samples: 100, Quality: 1, Compliance: 1, Reliability: 1, Updated: now},
		{"cloud", "remote", "code", "default"}: {Samples: 100, Quality: .8, Compliance: 1, Reliability: 1, Updated: now},
	}

	normal, err := Select(r, p, candidates, evidence, now, .9)
	if err != nil || normal.Primary.Model != "local" {
		t.Fatalf("normal selection: %+v %v", normal, err)
	}
	selected := map[string]bool{}
	for _, fraction := range []float64{.01, .51, .99} {
		out, err := Select(r, p, candidates, evidence, now, p.Exploration*fraction)
		if err != nil || !out.Explored || out.Primary.Model == normal.Primary.Model {
			t.Fatalf("fraction %.2f: %+v %v", fraction, out, err)
		}
		selected[out.Primary.Model] = true
	}
	if !selected["cloud"] || !selected["new-local"] {
		t.Fatalf("eligible alternatives not reachable: %v", selected)
	}
}

func TestExplorationCannotSelectFilteredCandidate(t *testing.T) {
	r, candidates, now := fixture()
	candidates[1].Healthy = false
	out, err := Select(r, Defaults(), candidates, nil, now, 0)
	if err != nil || out.Explored || out.Primary.Model != "local" || len(out.Excluded) != 1 || out.Excluded[0].Model != "cloud" {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestFallbackFailureDomains(t *testing.T) {
	r, c, now := fixture()
	same := c[0]
	same.Model = "second-local"
	secondCloud := c[1]
	secondCloud.Model = "second-cloud-a"
	otherCloud := c[1]
	otherCloud.Model, otherCloud.FailureDomain = "cloud-b", "cloud-b"
	unknown := c[1]
	unknown.Model, unknown.FailureDomain = "unknown", ""
	c = append(c, same, secondCloud, otherCloud, unknown)
	e := map[Key]Evidence{
		{"local", "ollama", "code", "default"}:          {Samples: 100, Quality: 1, Compliance: 1, Reliability: 1, Updated: now},
		{"second-local", "ollama", "code", "default"}:   {Samples: 100, Quality: .96, Compliance: 1, Reliability: 1, Updated: now},
		{"cloud", "remote", "code", "default"}:          {Samples: 100, Quality: .95, Compliance: 1, Reliability: 1, Updated: now},
		{"second-cloud-a", "remote", "code", "default"}: {Samples: 100, Quality: .94, Compliance: 1, Reliability: 1, Updated: now},
		{"cloud-b", "remote", "code", "default"}:        {Samples: 100, Quality: .93, Compliance: 1, Reliability: 1, Updated: now},
		{"unknown", "remote", "code", "default"}:        {Samples: 100, Quality: .92, Compliance: 1, Reliability: 1, Updated: now},
	}
	out, err := Select(r, Defaults(), c, e, now, .9)
	want := []string{"cloud", "cloud-b", "second-local", "second-cloud-a", "unknown"}
	got := make([]string, len(out.Fallbacks))
	for i := range out.Fallbacks {
		got[i] = out.Fallbacks[i].Model
	}
	if err != nil || out.Primary.Model != "local" || !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestInvalidAndEmptyRoutes(t *testing.T) {
	r, c, now := fixture()
	for _, draw := range []float64{-1, 1, math.NaN(), math.Inf(1)} {
		if _, err := Select(r, Defaults(), c, nil, now, draw); !errors.Is(err, ErrInvalid) {
			t.Fatal(err)
		}
	}
	c[0].Healthy = false
	c[1].Healthy = false
	if _, err := Select(r, Defaults(), c, nil, now, .9); !errors.Is(err, ErrNoRoute) {
		t.Fatal(err)
	}
	r, c, now = fixture()
	c = append(c, c[0])
	if _, err := Select(r, Defaults(), c, nil, now, .9); !errors.Is(err, ErrInvalid) {
		t.Fatal("duplicate accepted")
	}
}
