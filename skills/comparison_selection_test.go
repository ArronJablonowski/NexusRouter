package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"testing"
)

func TestComparisonCorrelatedSessionsExcludedAndDigestBound(t *testing.T) {
	p := comparisonFixturePolicy()
	input := comparisonFixtureOutcomes(20)
	ordinary, err := CompareTaskOutcomes(input, p)
	if err != nil {
		t.Fatal(err)
	}
	// Preserve the exact canonical pre-extension evidence digest when no global
	// session correlation observations have been supplied.
	body, _ := json.Marshal(struct {
		Policy   ComparisonPolicy `json:"policy"`
		Outcomes []TaskOutcome    `json:"outcomes"`
	}{p, input})
	sum := sha256.Sum256(body)
	if ordinary.EvidenceDigest != hex.EncodeToString(sum[:]) {
		t.Fatal("legacy digest changed")
	}
	explicitEmpty, err := CompareTaskOutcomesWithCorrelatedSessions(input, p, []string{})
	if err != nil || !reflect.DeepEqual(ordinary, explicitEmpty) {
		t.Fatal("empty correlation changed comparator", err)
	}
	ids := []string{input[21].SessionID, input[1].SessionID}
	before := append([]string(nil), ids...)
	excluded, err := CompareTaskOutcomesWithCorrelatedSessions(input, p, ids)
	if err != nil || excluded.Excluded["repeated_session"] != 2 || excluded.Baseline.Samples != 19 || excluded.Candidate.Samples != 19 || excluded.Status != "insufficient_evidence" || excluded.EvidenceDigest == ordinary.EvidenceDigest {
		t.Fatal(excluded, err)
	}
	if !reflect.DeepEqual(ids, before) {
		t.Fatal("caller session order mutated")
	}
	reversed, err := CompareTaskOutcomesWithCorrelatedSessions(input, p, []string{ids[1], ids[0]})
	if err != nil || !reflect.DeepEqual(excluded, reversed) {
		t.Fatal("session-order-dependent report", err)
	}
	for _, bad := range [][]string{{ids[0], ids[0]}, {"missing-session"}, {"bad/session"}, make([]string, 201)} {
		if _, err := CompareTaskOutcomesWithCorrelatedSessions(input, p, bad); err == nil {
			t.Fatal("invalid correlation accepted", bad)
		}
	}
}

func selectionFixtureReport(t *testing.T) ComparisonSelectionReport {
	t.Helper()
	p := comparisonFixturePolicy()
	c, err := CompareTaskOutcomes(comparisonFixtureOutcomes(20), p)
	if err != nil {
		t.Fatal(err)
	}
	return ComparisonSelectionReport{Version: 1, Policy: ComparisonSelectionPolicy{Version: 1, Comparison: p, Privacy: "local_only", TasksPerVersion: 20}, Watermark: 40, Baseline: ComparisonWindow{Selected: 20, OldestOrdinal: 1, NewestOrdinal: 20}, Candidate: ComparisonWindow{Selected: 20, OldestOrdinal: 21, NewestOrdinal: 40}, Comparison: &c}
}

func TestComparisonSelectionReportValidation(t *testing.T) {
	good := selectionFixtureReport(t)
	if good.Validate() != nil {
		t.Fatal("valid selection rejected")
	}
	good.Baseline.HasMore = true
	if good.Validate() != nil {
		t.Fatal("full latest window rejected")
	}
	for name, change := range map[string]func(*ComparisonSelectionReport){
		"schema": func(r *ComparisonSelectionReport) { r.Version = 2 }, "policy": func(r *ComparisonSelectionReport) { r.Policy.Version = 2 }, "privacy": func(r *ComparisonSelectionReport) { r.Policy.Privacy = "" }, "capacity": func(r *ComparisonSelectionReport) { r.Policy.TasksPerVersion = 101 }, "watermark": func(r *ComparisonSelectionReport) { r.Watermark = -1 }, "future": func(r *ComparisonSelectionReport) { r.Watermark = 39 }, "negative": func(r *ComparisonSelectionReport) { r.Baseline.Selected = -1 }, "overfull": func(r *ComparisonSelectionReport) { r.Baseline.Selected = 21 }, "has-more-short": func(r *ComparisonSelectionReport) { r.Baseline.Selected = 19 }, "inverted": func(r *ComparisonSelectionReport) { r.Baseline.OldestOrdinal = 21 }, "dense-impossible": func(r *ComparisonSelectionReport) { r.Baseline.OldestOrdinal = 2 }, "nil-comparison": func(r *ComparisonSelectionReport) { r.Comparison = nil }, "foreign-model": func(r *ComparisonSelectionReport) { r.ConfiguredModelID = "configured" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := good
			change(&bad)
			if bad.Validate() == nil {
				t.Fatal("invalid selection accepted")
			}
		})
	}
	empty := ComparisonSelectionReport{Version: 1, Policy: good.Policy, ConfiguredModelID: "configured"}
	if empty.Validate() != nil {
		t.Fatal("empty selection rejected")
	}
	empty.Comparison = good.Comparison
	if empty.Validate() == nil {
		t.Fatal("empty selection carried comparison")
	}
	empty.Comparison = nil
	empty.Baseline.NewestOrdinal = 1
	if empty.Validate() == nil {
		t.Fatal("empty selection carried ordinal")
	}
	empty.Baseline.NewestOrdinal = 0
	empty.Baseline.HasMore = true
	if empty.Validate() == nil {
		t.Fatal("empty selection reported truncation")
	}
}

func TestComparisonSelectionNestedPolicyAndCounts(t *testing.T) {
	for _, mode := range []string{"policy", "sampled", "cohort-count", "configured-model"} {
		t.Run(mode, func(t *testing.T) {
			r := selectionFixtureReport(t)
			switch mode {
			case "policy":
				r.Policy.Comparison.Key.Name = "other"
			case "sampled":
				r.Candidate.Selected = 19
				r.Candidate.OldestOrdinal = 22
			case "cohort-count":
				r.Baseline.Selected = 19
				r.Baseline.NewestOrdinal = 19
				r.Candidate.Selected = 21
				r.Candidate.OldestOrdinal = 20
				r.Policy.TasksPerVersion = 21
			case "configured-model":
				r.Comparison.ConfiguredModelID = "configured"
			}
			if r.Validate() == nil {
				t.Fatal("unbound nested report accepted")
			}
		})
	}
	r := selectionFixtureReport(t)
	r.ConfiguredModelID = "configured"
	r.Comparison.ConfiguredModelID = "configured"
	if r.Validate() != nil {
		t.Fatal("bound configured model rejected")
	}
}

func TestComparisonSelectionRequestValidation(t *testing.T) {
	p := comparisonFixturePolicy()
	r := ComparisonSelectionRequest{Version: 1, ModelID: "configured", Domain: p.Execution.Domain, Profile: p.Execution.Profile, Name: p.Key.Name, BaselineVersion: p.BaselineVersion, CandidateVersion: p.CandidateVersion, Source: p.Source, MinSamples: p.MinSamples, MinDrop: p.MinDrop, Privacy: "local_only", TasksPerVersion: 20}
	if r.Validate() != nil {
		t.Fatal("valid request rejected")
	}
	r.Privacy = "cloud_allowed"
	r.TasksPerVersion = 100
	if r.Validate() != nil {
		t.Fatal("maximum request rejected")
	}
	for _, n := range []int{0, 19, 101} {
		bad := r
		bad.TasksPerVersion = n
		if bad.Validate() == nil {
			t.Fatal("invalid capacity", n)
		}
	}
	for _, privacy := range []string{"", "shareable", "public"} {
		bad := r
		bad.Privacy = privacy
		if bad.Validate() == nil {
			t.Fatal("invalid privacy", privacy)
		}
	}
	r.CandidateVersion = r.BaselineVersion
	if r.Validate() == nil {
		t.Fatal("same version accepted")
	}
}
