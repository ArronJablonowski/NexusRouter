package app

import (
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
)

func TestNativeEvaluationRequiresOptInAndHonorsEligibility(t *testing.T) {
	now := time.Now().UTC()
	snapshot, err := harness.Replay(nil, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	base := harness.Identity{Version: 1, Harness: "pi", HarnessVersion: "1", AdapterVersion: "1", Provider: "local", Model: "alpha", ModelRevision: "1", ConfigSHA256: strings.Repeat("a", 64)}
	second := base
	second.Model = "beta"
	candidates := []harness.Candidate{}
	for _, id := range []harness.Identity{base, second} {
		candidates = append(candidates, harness.Candidate{Identity: id, Local: true, Available: true, Authorized: true, Compatible: true, CapacityAvailable: true, CredentialAvailable: true, ContextTokens: 8192})
	}
	for _, tc := range []struct {
		name       string
		opt        bool
		rate, draw float64
		want       bool
	}{
		{"ordinary", false, .25, 0, false}, {"evaluation", true, .25, 0, true}, {"disabled", true, 0, 0, false}, {"outside_probability", true, .25, .25, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Service{settings: config.Defaults()}
			s.settings.Mode = "local_only"
			s.settings.Routing.Exploration = tc.rate
			draws := 0
			s.draw = func() float64 { draws++; return tc.draw }
			r := Request{HarnessID: "auto", ModelID: "auto", HarnessEvaluation: tc.opt, Domain: "coding", Profile: "tests-v1", ContextTokens: 8192}
			choose := s.nativeRouteSelector(r, snapshot, now)
			first, e := choose(candidates)
			if e != nil || first.Explored != tc.want {
				t.Fatal(first, e)
			}
			if tc.want && (first.Primary.Identity != second || first.Reason != "explicit_bounded_evaluation") {
				t.Fatal(first)
			}
			if !tc.want && first.Primary.Identity != base {
				t.Fatal(first)
			}
			excluded := append([]harness.Candidate(nil), candidates...)
			excluded[1].Authorized = false
			again, e := choose(excluded)
			if e != nil || again.Explored || again.Primary.Identity != base || len(again.Excluded) != 1 {
				t.Fatal("exploration bypassed authority", again, e)
			}
			wantDraws := 0
			if tc.opt && tc.rate > 0 {
				wantDraws = 1
			}
			if draws != wantDraws {
				t.Fatal("reranking consumed another draw", draws)
			}
		})
	}
	s := &Service{}
	for _, id := range []string{"", "pi-local"} {
		if _, e := s.bindNativeHarness(Request{HarnessID: id, HarnessEvaluation: true}); e == nil {
			t.Fatal("evaluation on non-auto route")
		}
	}
}
