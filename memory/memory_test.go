package memory

import (
	"math"
	"testing"
	"time"
)

func TestValidation(t *testing.T) {
	now := time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)
	f := Fact{Version: 1, ID: "id", Scope: "scope", Revision: 1, Content: "fact", Provenance: "user", Confidence: 1, Privacy: "local_only", Created: now, Updated: now}
	if err := f.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Fact){func(f *Fact) { f.Confidence = math.NaN() }, func(f *Fact) { f.Privacy = "" }, func(f *Fact) { f.Provenance = "" }, func(f *Fact) { f.Expires = now }, func(f *Fact) { f.Scope = "" }, func(f *Fact) { f.Updated = now.Add(-time.Second) }} {
		bad := f
		mutate(&bad)
		if bad.Validate() == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	if (Query{Scope: "scope", Now: now, Limit: 1001}).Validate() == nil {
		t.Fatal("unbounded query")
	}
}

func TestTimeBoundariesUseUTC(t *testing.T) {
	west := time.FixedZone("west", -7*60*60)
	east := time.FixedZone("east", 7*60*60)
	if !ValidTime(time.Unix(100, 0).In(west)) {
		t.Fatal("rejected valid UTC instant represented in 1969")
	}
	if ValidTime(time.Date(2261, 1, 1, 0, 0, 0, 0, time.UTC).In(west)) {
		t.Fatal("accepted out-of-range UTC instant represented in 2260")
	}
	if ValidTime(time.Date(1969, 12, 31, 23, 59, 0, 0, time.UTC).In(east)) {
		t.Fatal("accepted pre-epoch UTC instant represented in 1970")
	}
}
