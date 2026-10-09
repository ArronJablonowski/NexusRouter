package harness

import (
	"github.com/ArronJablonowski/NexusRouter/routing"
	"testing"
)

func TestRoutingObservationProjectionUsesExactHeadsAndScope(t *testing.T) {
	i := identity("coder", "pi")
	e, passed := observation("attempt", i, testClass, true)
	failed := passed
	failed.ID = "correction"
	failed.ExpectedHead = passed.ID
	failed.Verdict = "failed"
	failed.Quality = 1
	snap := snapshot(t, []Execution{e}, []Review{passed, failed})
	key := routing.Key{Model: i.Model, Provider: "paired-host-identity", Domain: testClass.Domain, Profile: testClass.Profile}
	got := snap.RoutingObservations(i, testClass, key)
	if len(got.Fitness) != 1 || got.Fitness[0].Quality != 0 || got.Fitness[0].Time != e.CompletedAt {
		t.Fatal("old vote or quality overrode corrected accuracy", got)
	}
	other := key
	other.Profile = "wrong"
	if len(snap.RoutingObservations(i, testClass, other).Fitness) != 0 {
		t.Fatal("borrowed another profile")
	}
	revision := i
	revision.ModelRevision = "new-weights"
	if len(snap.RoutingObservations(revision, testClass, key).Fitness) != 0 {
		t.Fatal("borrowed another model revision")
	}
	withdrawn := failed
	withdrawn.ID = "withdrawal"
	withdrawn.ExpectedHead = failed.ID
	withdrawn.Verdict = "withdrawn"
	withdrawn.Method = ""
	withdrawn.MethodVersion = ""
	withdrawn.Quality = 0
	withdrawn.Confidence = 0
	snap = snapshot(t, []Execution{e}, []Review{passed, failed, withdrawn})
	got = snap.RoutingObservations(i, testClass, key)
	if len(got.Fitness) != 0 || len(got.Advisory) != 0 {
		t.Fatal("withdrawn evidence retained", got)
	}
}
func TestRoutingObservationProjectionKeepsUncertainReviewAdvisory(t *testing.T) {
	i := identity("coder", "pi")
	e, r := observation("attempt", i, testClass, true)
	key := routing.Key{Model: i.Model, Provider: "paired", Domain: testClass.Domain, Profile: testClass.Profile}
	for _, method := range []string{"automated_ai", "human"} {
		r.Method = method
		r.Confidence = .6
		got := snapshot(t, []Execution{e}, []Review{r}).RoutingObservations(i, testClass, key)
		if len(got.Fitness) != 0 || len(got.Advisory) != 1 || got.Advisory[0].Confidence != .6 {
			t.Fatal(got)
		}
	}
	got := snapshot(t, []Execution{e}, nil).RoutingObservations(i, testClass, key)
	if len(got.Fitness) != 0 || len(got.Advisory) != 0 {
		t.Fatal("completion became accuracy", got)
	}
}
