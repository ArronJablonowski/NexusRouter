package harness

import (
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"
)

func TestReplayOrderingAndCallerMutationCannotChangeSelection(t *testing.T) {
	a := identity("actual-fallback", "hermes")
	b := identity("requested", "pi")
	var es []Execution
	var rs []Review
	for n := range 80 {
		e, r := observation(fmt.Sprint(n), a, testClass, n%3 != 0)
		e.CompletedAt = testNow.Add(-time.Duration(n+1) * time.Hour)
		r.ExecutionDigest, _ = e.Digest()
		r.Confidence = float64(n+1) / 80
		es, rs = append(es, e), append(rs, r)
	}
	cs := []Candidate{candidate(a), candidate(b)}
	s := snapshot(t, es, rs)
	want := selectRoute(t, request(), DefaultPolicy(), cs, s, .9)
	for range 30 {
		slices.Reverse(es)
		slices.Reverse(rs)
		got := selectRoute(t, request(), DefaultPolicy(), cs, snapshot(t, es, rs), .9)
		if !reflect.DeepEqual(want, got) {
			t.Fatal("replay order changed floating-point scores")
		}
	}
	for n := range es {
		es[n].Actual = b
		rs[n].Verdict = "failed"
	}
	got := selectRoute(t, request(), DefaultPolicy(), cs, s, .9)
	if !reflect.DeepEqual(want, got) || got.Primary.Identity != a {
		t.Fatal("snapshot borrowed mutable caller evidence")
	}
	for _, r := range got.Ranked {
		if r.Identity == b && r.EffectiveSamples != 0 {
			t.Fatal("requested route inherited actual fallback quality")
		}
	}
}

func TestRecentRegressionsOutweighStaleSuccesses(t *testing.T) {
	a, b := identity("regressed", "pi"), identity("stable", "goose")
	var es []Execution
	var rs []Review
	for n := range 100 {
		e, r := observation(fmt.Sprint(n), a, testClass, n < 90)
		if n < 90 {
			e.CompletedAt = testNow.Add(-365 * 24 * time.Hour)
			r.ExecutionDigest, _ = e.Digest()
		}
		es, rs = append(es, e), append(rs, r)
	}
	e, r := observation("stable", b, testClass, true)
	es, rs = append(es, e), append(rs, r)
	got := selectRoute(t, request(), DefaultPolicy(), []Candidate{candidate(a), candidate(b)}, snapshot(t, es, rs), .9)
	if got.Primary.Identity != b {
		t.Fatal("stale aggregate hid recent regression")
	}
}

func TestNoncanonicalTimestampsFailBeforeHashing(t *testing.T) {
	a := identity("a", "pi")
	for _, bad := range []time.Time{time.Time{}, time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), testNow.In(time.FixedZone("offset", 3600))} {
		e, r := observation("bad-time", a, testClass, true)
		e.CompletedAt = bad
		if _, err := e.Digest(); err != ErrInvalid {
			t.Fatal("invalid execution timestamp accepted")
		}
		r.CreatedAt = bad
		if r.Validate() != ErrInvalid {
			t.Fatal("invalid review timestamp accepted")
		}
		if _, err := Replay(nil, nil, bad); err != ErrInvalid {
			t.Fatal("invalid replay timestamp accepted")
		}
	}
}
