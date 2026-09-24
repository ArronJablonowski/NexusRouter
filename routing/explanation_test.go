package routing

import (
	"testing"
	"time"
)

func explanationFixture(t *testing.T) ([]Candidate, Policy, Selection) {
	t.Helper()
	policy := Defaults()
	policy.Exploration = 0
	candidates := []Candidate{
		{Model: "a", Provider: "local", FailureDomain: "host-a", Local: true, Capabilities: []string{"chat"}, ContextTokens: 8192, Healthy: true, PolicyAllowed: true, CapacityAvailable: true},
		{Model: "b", Provider: "cloud", FailureDomain: "host-b", Capabilities: []string{"chat"}, ContextTokens: 8192, Healthy: true, PolicyAllowed: true, CapacityAvailable: true},
		{Model: "c", Provider: "cloud", FailureDomain: "host-c", Capabilities: []string{"chat"}, ContextTokens: 8192, Healthy: false, PolicyAllowed: true, CapacityAvailable: true},
	}
	selection, err := Select(Request{Mode: "hybrid", Domain: "code", Profile: "default", Capabilities: []string{"chat"}, ContextTokens: 10, MaxCost: 1}, policy, candidates, nil, time.Unix(1000, 0), .5)
	if err != nil {
		t.Fatal(err)
	}
	return candidates, policy, selection
}

func TestValidateExplanation(t *testing.T) {
	candidates, policy, selection := explanationFixture(t)
	if err := ValidateExplanation(candidates, &policy, &selection); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*[]Candidate, *Policy, *Selection)
	}{
		{"duplicate candidate", func(c *[]Candidate, _ *Policy, _ *Selection) { (*c)[1] = (*c)[0] }},
		{"bad policy", func(_ *[]Candidate, p *Policy, _ *Selection) { p.Weights.Quality = 2 }},
		{"missing primary", func(_ *[]Candidate, _ *Policy, s *Selection) { s.Primary.Model = "other" }},
		{"bad order", func(_ *[]Candidate, _ *Policy, s *Selection) { s.Ranked[0], s.Ranked[1] = s.Ranked[1], s.Ranked[0] }},
		{"missing fallback", func(_ *[]Candidate, _ *Policy, s *Selection) { s.Fallbacks = nil }},
		{"forged fallback", func(_ *[]Candidate, _ *Policy, s *Selection) { s.Fallbacks[0].Score = .99 }},
		{"unknown exclusion", func(_ *[]Candidate, _ *Policy, s *Selection) { s.Excluded[0].Model = "other" }},
		{"unknown reason", func(_ *[]Candidate, _ *Policy, s *Selection) { s.Excluded[0].Reasons = []string{"secret"} }},
		{"reordered reasons", func(_ *[]Candidate, _ *Policy, s *Selection) { s.Excluded[0].Reasons = []string{"capacity", "health"} }},
		{"invalid score", func(_ *[]Candidate, _ *Policy, s *Selection) { s.Ranked[0].Score = 2 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := append([]Candidate(nil), candidates...)
			p := policy
			s := selection
			s.Ranked = append([]Ranked(nil), selection.Ranked...)
			s.Fallbacks = append([]Ranked(nil), selection.Fallbacks...)
			s.Excluded = append([]Exclusion(nil), selection.Excluded...)
			for i := range s.Excluded {
				s.Excluded[i].Reasons = append([]string(nil), s.Excluded[i].Reasons...)
			}
			tc.mutate(&c, &p, &s)
			if ValidateExplanation(c, &p, &s) == nil {
				t.Fatal("invalid explanation accepted")
			}
		})
	}
}

func TestValidateExplanationRejectsReorderedFallbacks(t *testing.T) {
	candidates, policy, _ := explanationFixture(t)
	candidates[2].Healthy = true
	selection, err := Select(Request{Mode: "hybrid", Domain: "code", Profile: "default", Capabilities: []string{"chat"}, ContextTokens: 10, MaxCost: 1}, policy, candidates, nil, time.Unix(1000, 0), .5)
	if err != nil || len(selection.Fallbacks) != 2 {
		t.Fatal(selection, err)
	}
	selection.Fallbacks[0], selection.Fallbacks[1] = selection.Fallbacks[1], selection.Fallbacks[0]
	if ValidateExplanation(candidates, &policy, &selection) == nil {
		t.Fatal("reordered fallback accepted")
	}
}

func TestValidateExploredExplanation(t *testing.T) {
	candidates, policy, _ := explanationFixture(t)
	policy.Exploration = .25
	selection, err := Select(Request{Mode: "hybrid", Domain: "code", Profile: "default", Capabilities: []string{"chat"}, ContextTokens: 10, MaxCost: 1}, policy, candidates, nil, time.Unix(1000, 0), .1)
	if err != nil || !selection.Explored || ValidateExplanation(candidates, &policy, &selection) != nil {
		t.Fatal(selection, err)
	}
}

func TestValidateExplanationRejectsOverconfidentLegacyPrior(t *testing.T) {
	candidates, policy, _ := explanationFixture(t)
	now := time.Unix(1000, 0)
	request := Request{Mode: "hybrid", Domain: "code", Profile: "default", Capabilities: []string{"chat"}, ContextTokens: 10, MaxCost: 1}
	evidence := map[Key]Evidence{{Model: "a", Provider: "local", Domain: "code", Profile: "default"}: {
		SourceDomain: "coding", SourceProfile: "benchmark", Samples: 100,
		Quality: 1, Reliability: 1, Updated: now,
	}}
	selection, err := Select(request, policy, candidates, evidence, now, .5)
	if err != nil || ValidateExplanation(candidates, &policy, &selection) != nil {
		t.Fatal(selection, err)
	}
	selection.Ranked[0].Confidence = .8
	selection.Ranked[0].Uncertainty = .2
	selection.Primary = selection.Ranked[0]
	if ValidateExplanation(candidates, &policy, &selection) == nil {
		t.Fatal("transferred evidence exceeded prior confidence cap")
	}
}
