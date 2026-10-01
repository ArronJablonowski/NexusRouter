package harness

import (
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
var testClass = TaskClass{Domain: "coding", Profile: "unit-tests-v1", Difficulty: "hard"}

func identity(model, harness string) Identity {
	return Identity{Version: 1, Harness: harness, HarnessVersion: "1.0", AdapterVersion: "jsonl-v1", Provider: "local", Model: model, ModelRevision: "weights-1", ConfigSHA256: strings.Repeat("a", 64)}
}
func candidate(i Identity) Candidate {
	return Candidate{Identity: i, Local: true, Available: true, Authorized: true, Compatible: true, CapacityAvailable: true, CredentialAvailable: true, ContextTokens: 32768, EstimatedCost: 1, Capabilities: []string{"text"}}
}
func request() Request {
	return Request{Version: 1, Task: testClass, Mode: "local_only", Capabilities: []string{"text"}, ContextTokens: 4096, MaxCost: 10}
}
func observation(id string, i Identity, task TaskClass, pass bool) (Execution, Review) {
	e := Execution{Version: 1, ID: id, Actual: i, Task: task, Status: "completed", OutputSHA256: strings.Repeat("b", 64), CompletedAt: testNow.Add(-time.Minute)}
	d, _ := e.Digest()
	v := "failed"
	if pass {
		v = "passed"
	}
	return e, Review{Version: 1, ID: id + "-review", ExecutionDigest: d, Verdict: v, Method: "deterministic", MethodVersion: "tests-v1", Reviewer: "trusted-host", Confidence: 1, Quality: 1, CreatedAt: testNow}
}
func snapshot(t *testing.T, es []Execution, rs []Review) *Snapshot {
	t.Helper()
	s, err := Replay(es, rs, testNow)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func selectRoute(t *testing.T, r Request, p Policy, cs []Candidate, s *Snapshot, draw float64) Selection {
	t.Helper()
	v, err := Select(r, p, cs, s, testNow, draw)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// Training records and held-out request identities are disjoint. The same
// model reverses rank across harnesses; no model-only or harness-only winner
// can represent this interaction table. These are controlled policy tests,
// not a claim of measured real-harness accuracy.
func TestHeldOutCombinationInteractionsAndTaskSpecialization(t *testing.T) {
	a, b, c, d := identity("model-a", "pi"), identity("model-a", "goose"), identity("model-b", "pi"), identity("model-b", "goose")
	cs := []Candidate{candidate(a), candidate(b), candidate(c), candidate(d)}
	var es []Execution
	var rs []Review
	translation := TaskClass{Domain: "translation", Profile: "rubric-v1", Difficulty: "hard"}
	for _, task := range []TaskClass{testClass, translation} {
		for j, i := range []Identity{a, b, c, d} {
			for k := range 12 {
				pass := j == 0
				if task == translation {
					pass = j == 3
				}
				e, r := observation(fmt.Sprintf("train-%s-%d-%d", task.Domain, j, k), i, task, pass)
				es = append(es, e)
				rs = append(rs, r)
			}
		}
	}
	s := snapshot(t, es, rs)
	q := request()
	v := selectRoute(t, q, DefaultPolicy(), cs, s, .9)
	if v.Primary.Identity != a {
		t.Fatal("coding interaction lost", v.Primary)
	}
	q.Task = translation
	v = selectRoute(t, q, DefaultPolicy(), cs, s, .9)
	if v.Primary.Identity != d {
		t.Fatal("translation specialization lost", v.Primary)
	}
	q.Task.Difficulty = "easy"
	v = selectRoute(t, q, DefaultPolicy(), cs, s, .9)
	if v.Primary.EffectiveSamples != 0 || v.Reason != "insufficient_evidence_stable_tiebreak" {
		t.Fatal("borrowed hard-task evidence")
	}
}
func TestAccuracyDominatesPriceAndQuality(t *testing.T) {
	a, b := identity("accurate", "pi"), identity("cheap", "pi")
	ca, cb := candidate(a), candidate(b)
	ca.EstimatedCost = 10
	cb.EstimatedCost = 0
	ea, ra := observation("accurate", a, testClass, true)
	ra.Quality = .1
	eb, rb := observation("cheap", b, testClass, false)
	rb.Quality = 1
	v := selectRoute(t, request(), DefaultPolicy(), []Candidate{cb, ca}, snapshot(t, []Execution{ea, eb}, []Review{ra, rb}), .9)
	if v.Primary.Identity != a {
		t.Fatal("cost/quality outweighed correctness")
	}
}
func TestVersionAndConfigurationIsolation(t *testing.T) {
	a := identity("a", "pi")
	e, r := observation("old", a, testClass, true)
	s := snapshot(t, []Execution{e}, []Review{r})
	variants := []Identity{a, a, a, a, a}
	variants[0].ModelRevision = "weights-2"
	variants[1].HarnessVersion = "2.0"
	variants[2].ConfigSHA256 = strings.Repeat("c", 64)
	variants[3].AdapterVersion = "v2"
	variants[4].Provider = "other"
	for _, i := range variants {
		v := selectRoute(t, request(), DefaultPolicy(), []Candidate{candidate(i)}, s, .9)
		if v.Primary.ConfirmedSamples != 0 {
			t.Fatal("borrowed prior identity evidence")
		}
	}
}
func TestExecutionSuccessInfrastructureAndUnverifiedAreNotQuality(t *testing.T) {
	a := identity("a", "hermes")
	var es []Execution
	for k, status := range []string{"completed", "infrastructure_failed", "canceled", "indeterminate"} {
		e, _ := observation(fmt.Sprint(k), a, testClass, true)
		e.Status = status
		es = append(es, e)
	}
	v := selectRoute(t, request(), DefaultPolicy(), []Candidate{candidate(a)}, snapshot(t, es, nil), .9)
	if v.Primary.Correctness != .5 || v.Primary.EffectiveSamples != 0 || v.Primary.PendingOutputs != 1 || v.Primary.InfrastructureFailures != 1 || v.Primary.Canceled != 1 || v.Primary.Indeterminate != 1 {
		t.Fatal(v.Primary)
	}
	for _, e := range es[1:] {
		d, _ := e.Digest()
		_, r := observation("review", a, testClass, true)
		r.ExecutionDigest = d
		if _, err := Replay(es, []Review{r}, testNow); err == nil {
			t.Fatal("quality vote on failed lineage")
		}
	}
}
func TestRevisionsWithdrawalsAndExactReplay(t *testing.T) {
	a, b := identity("a", "pi"), identity("b", "pi")
	e, r := observation("task", a, testClass, true)
	q := request()
	cs := []Candidate{candidate(a), candidate(b)}
	if v := selectRoute(t, q, DefaultPolicy(), cs, snapshot(t, []Execution{e, e}, []Review{r, r}), .9); v.Primary.Identity != a || v.Primary.ConfirmedSamples != 1 {
		t.Fatal("retry inflated evidence")
	}
	revised := r
	revised.ID = "revision"
	revised.ExpectedHead = r.ID
	revised.Verdict = "failed"
	s := snapshot(t, []Execution{e}, []Review{r, revised})
	if v := selectRoute(t, q, DefaultPolicy(), cs, s, .9); v.Primary.Identity != b {
		t.Fatal("new outcome did not change rank")
	}
	stale := revised
	stale.ID = "stale"
	if _, err := Replay([]Execution{e}, []Review{r, revised, stale}, testNow); err != ErrConflict {
		t.Fatal("stale head accepted", err)
	}
	withdrawn := Review{Version: 1, ID: "withdraw", ExecutionDigest: r.ExecutionDigest, ExpectedHead: revised.ID, Verdict: "withdrawn", Reviewer: "operator", CreatedAt: testNow}
	s = snapshot(t, []Execution{e}, []Review{r, revised, withdrawn})
	v := selectRoute(t, q, DefaultPolicy(), []Candidate{candidate(a)}, s, .9)
	if v.Primary.ConfirmedSamples != 0 || v.Primary.EffectiveSamples != 0 {
		t.Fatal("withdrawal still contributes")
	}
	conflicting := r
	conflicting.Quality = .2
	if _, err := Replay([]Execution{e}, []Review{r, conflicting}, testNow); err != ErrConflict {
		t.Fatal("changed review ID accepted")
	}
}
func TestEveryHardConstraintFiltersBeforeRanking(t *testing.T) {
	a := identity("a", "openhands")
	e, r := observation("best", a, testClass, true)
	s := snapshot(t, []Execution{e}, []Review{r})
	cases := map[string]func(*Candidate){"mode": func(c *Candidate) { c.Local = false }, "unavailable": func(c *Candidate) { c.Available = false }, "authorization": func(c *Candidate) { c.Authorized = false }, "incompatible": func(c *Candidate) { c.Compatible = false }, "capacity": func(c *Candidate) { c.CapacityAvailable = false }, "credential": func(c *Candidate) { c.CredentialAvailable = false }, "context": func(c *Candidate) { c.ContextTokens = 100 }, "budget": func(c *Candidate) { c.EstimatedCost = 11 }, "capability": func(c *Candidate) { c.Capabilities = nil }}
	for reason, change := range cases {
		t.Run(reason, func(t *testing.T) {
			c := candidate(a)
			change(&c)
			v, err := Select(request(), DefaultPolicy(), []Candidate{c}, s, testNow, .9)
			if err != ErrNoRoute || len(v.Excluded) != 1 || !reflect.DeepEqual(v.Excluded[0].Reasons, []string{reason}) {
				t.Fatal(v, err)
			}
		})
	}
}
func TestExplorationRequiresExplicitEvaluationAndIsBounded(t *testing.T) {
	a, b := identity("proven", "pi"), identity("new", "hermes")
	e, r := observation("train", a, testClass, true)
	s := snapshot(t, []Execution{e}, []Review{r})
	cs := []Candidate{candidate(a), candidate(b)}
	p := DefaultPolicy()
	p.Exploration = .1
	q := request()
	if v := selectRoute(t, q, p, cs, s, 0); v.Explored || v.Primary.Identity != a {
		t.Fatal("silent exploration")
	}
	q.AllowExploration = true
	v := selectRoute(t, q, p, cs, s, 0)
	if !v.Explored || v.Primary.Identity != b {
		t.Fatal("no bounded evaluation")
	}
	if v = selectRoute(t, q, p, cs, s, .1); v.Explored {
		t.Fatal("exploration exceeded probability")
	}
	p.Exploration = .251
	if _, err := Select(q, p, cs, s, testNow, 0); err != ErrInvalid {
		t.Fatal("unbounded exploration")
	}
}
func TestDecayAndAutomatedReviewRemainUncertain(t *testing.T) {
	a := identity("a", "pi")
	e, r := observation("auto", a, testClass, true)
	r.Method = "automated_ai"
	e.CompletedAt = testNow.Add(-30 * 24 * time.Hour)
	r.ExecutionDigest, _ = e.Digest()
	v := selectRoute(t, request(), DefaultPolicy(), []Candidate{candidate(a)}, snapshot(t, []Execution{e}, []Review{r}), .9)
	if v.Primary.ConfirmedSamples != 0 || v.Primary.AdvisorySamples != 1 || math.Abs(v.Primary.EffectiveSamples-.125) > 1e-9 {
		t.Fatal(v.Primary)
	}
}
func TestInvalidInputsFailClosed(t *testing.T) {
	a := identity("a", "pi")
	s := snapshot(t, nil, nil)
	q := request()
	p := DefaultPolicy()
	cs := []Candidate{candidate(a)}
	for _, bad := range []float64{math.NaN(), math.Inf(1), -1, 1} {
		if _, err := Select(q, p, cs, s, testNow, bad); err != ErrInvalid {
			t.Fatal("bad draw", bad)
		}
	}
	if _, err := Select(q, p, append(cs, cs[0]), s, testNow, 0); err != ErrInvalid {
		t.Fatal("duplicate identity")
	}
	if _, err := Select(q, p, cs, &Snapshot{}, testNow, 0); err != ErrInvalid {
		t.Fatal("zero snapshot")
	}
	e, r := observation("future", a, testClass, true)
	e.CompletedAt = testNow.Add(time.Hour)
	if _, err := Replay([]Execution{e}, nil, testNow); err != ErrInvalid {
		t.Fatal("future execution")
	}
	e, r = observation("x", a, testClass, true)
	r.ExecutionDigest = strings.Repeat("c", 64)
	if _, err := Replay([]Execution{e}, []Review{r}, testNow); err != ErrConflict {
		t.Fatal("unbound output")
	}
}
