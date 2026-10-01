package harness

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

func TestPersistedSelectionBindings(t *testing.T) {
	a, b := identity("a", "pi"), identity("b", "pi")
	selected := selectRoute(t, request(), DefaultPolicy(), []Candidate{candidate(a), candidate(b)}, snapshot(t, nil, nil), .5)
	if selected.Validate() != nil {
		t.Fatal("selector emitted invalid decision")
	}
	for _, mutate := range []func(*Selection){
		func(s *Selection) { s.Primary.Identity.Model = "other" },
		func(s *Selection) { s.Task.Difficulty = "invented" },
		func(s *Selection) { s.Reason = "guaranteed_best" },
		func(s *Selection) { s.Explored = true },
		func(s *Selection) { s.Ranked[0].Correctness = math.NaN() },
		func(s *Selection) { s.Ranked[0].PendingOutputs = -1 },
		func(s *Selection) { s.Ranked[0].LastEvidence = s.AsOf.Add(time.Second) },
		func(s *Selection) { s.Ranked = append(s.Ranked, s.Ranked[0]) },
		func(s *Selection) {
			s.Excluded = append(s.Excluded, Exclusion{Identity: s.Primary.Identity, Reasons: []string{"capacity"}})
		},
	} {
		body, _ := json.Marshal(selected)
		var changed Selection
		if err := json.Unmarshal(body, &changed); err != nil {
			t.Fatal(err)
		}
		mutate(&changed)
		if changed.Validate() == nil {
			t.Fatal("invalid persisted decision accepted", changed)
		}
	}
}
