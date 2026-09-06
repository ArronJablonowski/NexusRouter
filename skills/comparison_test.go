package skills

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/routing"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func comparisonFixturePolicy() ComparisonPolicy {
	return ComparisonPolicy{Key: Key{"project", "lookup"}, BaselineVersion: strings.Repeat("a", 32), CandidateVersion: strings.Repeat("b", 32), Execution: routing.Key{Model: "model:tag", Provider: "local", Domain: "creative", Profile: "default"}, Source: evaluation.UserFeedback, MinSamples: 20, MinDrop: .1}
}
func comparisonFixtureOutcome(i int, candidate, accepted bool) TaskOutcome {
	p := comparisonFixturePolicy()
	v, d := p.BaselineVersion, strings.Repeat("c", 64)
	if candidate {
		v, d = p.CandidateVersion, strings.Repeat("d", 64)
	}
	return TaskOutcome{Version: 1, TaskID: fmt.Sprintf("task-%03d", i), SessionID: fmt.Sprintf("session-%03d", i), State: "completed", Sequence: 4, SkillContext: &runtime.SkillContextUse{Version: 1, Complete: true, References: []runtime.SkillReference{{Scope: p.Key.Scope, Name: p.Key.Name, Version: v, Digest: d}}}, AttemptID: "attempt", Key: &p.Execution, EvaluationID: "evaluation", EvaluationDigest: strings.Repeat("e", 64), Quality: &evaluation.Outcome{Source: p.Source, Accepted: accepted, References: []string{"user"}}, OutputChecks: []TaskOutputCheck{}}
}
func comparisonFixtureOutcomes(n int) []TaskOutcome {
	var out []TaskOutcome
	for i := 0; i < 2*n; i++ {
		out = append(out, comparisonFixtureOutcome(i, i >= n, i < n))
	}
	return out
}

func TestComparisonSignalPermutationOwnership(t *testing.T) {
	input := comparisonFixtureOutcomes(20)
	policy := comparisonFixturePolicy()
	before, _ := json.Marshal(input)
	got, err := CompareTaskOutcomes(input, policy)
	if err != nil || got.Status != "regression_signal" || !got.AdvisoryOnly || got.Baseline.Samples != 20 || got.Candidate.Samples != 20 || got.Baseline.Accepted != 20 || got.Candidate.Accepted != 0 {
		t.Fatal(got, err)
	}
	after, _ := json.Marshal(input)
	if string(before) != string(after) {
		t.Fatal("mutated input")
	}
	for i, j := 0, len(input)-1; i < j; i, j = i+1, j-1 {
		input[i], input[j] = input[j], input[i]
	}
	other, err := CompareTaskOutcomes(input, policy)
	if err != nil || !reflect.DeepEqual(got, other) {
		t.Fatal("permutation changed report", err)
	}
	input[0].SkillContext.References[0].Name = "changed"
	input[0].Quality.References[0] = "changed"
	if got.Policy.Key.Name != "lookup" || got.Validate() != nil {
		t.Fatal("report aliases input")
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), `"references"`) || strings.Contains(string(raw), "session-") || strings.Contains(string(raw), "task-") {
		t.Fatal("raw attribution/evidence escaped")
	}
}

func TestComparisonInsufficientAndNoSignal(t *testing.T) {
	p := comparisonFixturePolicy()
	small, err := CompareTaskOutcomes(comparisonFixtureOutcomes(19), p)
	if err != nil || small.Status != "insufficient_evidence" {
		t.Fatal(small, err)
	}
	all := comparisonFixtureOutcomes(20)
	for i := range all {
		all[i].Quality.Accepted = true
	}
	same, err := CompareTaskOutcomes(all, p)
	if err != nil || same.Status != "no_regression_signal" {
		t.Fatal(same, err)
	}
	for i := range all {
		all[i].Quality = nil
		all[i].EvaluationID = ""
		all[i].EvaluationDigest = ""
		all[i].OutputChecks = []TaskOutputCheck{{EventID: "mechanical", Code: "deterministic.nonempty_text.v1", Passed: true}}
	}
	unknown, err := CompareTaskOutcomes(all, p)
	if err != nil || unknown.Excluded["unknown_quality"] != 40 || unknown.Baseline.Samples != 0 || unknown.Baseline.Lower != 0 || unknown.Baseline.Upper != 1 || unknown.Status != "insufficient_evidence" {
		t.Fatal(unknown, err)
	}
}

func TestComparisonExclusionsAndDigestConflicts(t *testing.T) {
	mutations := map[string]func(*TaskOutcome){
		"related_task":          func(o *TaskOutcome) { o.ParentTaskID = "parent" },
		"nonfinal_outcome":      func(o *TaskOutcome) { o.State = "canceled" },
		"unknown_attribution":   func(o *TaskOutcome) { o.SkillContext = nil },
		"ambiguous_attribution": func(o *TaskOutcome) { o.SkillContext.References = nil },
		"other_skill":           func(o *TaskOutcome) { o.SkillContext.References[0].Name = "other" },
		"other_execution":       func(o *TaskOutcome) { o.Key.Profile = "other" },
		"unknown_quality":       func(o *TaskOutcome) { o.Quality = nil; o.EvaluationID = ""; o.EvaluationDigest = "" },
		"other_source":          func(o *TaskOutcome) { o.Quality.Source = evaluation.LLMJudge },
	}
	for reason, mutate := range mutations {
		t.Run(reason, func(t *testing.T) {
			o := comparisonFixtureOutcome(1, false, true)
			mutate(&o)
			r, err := CompareTaskOutcomes([]TaskOutcome{o}, comparisonFixturePolicy())
			if err != nil || r.Excluded[reason] != 1 || r.Baseline.Samples != 0 {
				t.Fatal(r, err)
			}
		})
	}
	a, b := comparisonFixtureOutcome(1, false, true), comparisonFixtureOutcome(2, true, false)
	b.SessionID = a.SessionID
	r, err := CompareTaskOutcomes([]TaskOutcome{a, b}, comparisonFixturePolicy())
	if err != nil || r.Excluded["repeated_session"] != 2 {
		t.Fatal(r, err)
	}
	b.SkillContext.References[0].Version = a.SkillContext.References[0].Version
	if _, err := CompareTaskOutcomes([]TaskOutcome{a, b}, comparisonFixturePolicy()); err == nil {
		t.Fatal("digest conflict hidden by repeated session exclusion")
	}
	b = a
	if _, err := CompareTaskOutcomes([]TaskOutcome{a, b}, comparisonFixturePolicy()); err == nil {
		t.Fatal("duplicate task accepted")
	}
}

func TestComparisonWilsonBoundsAndReportValidation(t *testing.T) {
	for n := 1; n <= 200; n++ {
		for accepted := 0; accepted <= n; accepted++ {
			c := ComparisonCohort{Samples: n, Accepted: accepted}
			setComparisonInterval(&c)
			if c.Lower < 0 || c.Upper > 1 || c.Lower > c.Rate+1e-15 || c.Upper < c.Rate-1e-15 {
				t.Fatal(c)
			}
		}
	}
	c := ComparisonCohort{Samples: 20, Accepted: 4}
	setComparisonInterval(&c)
	if math.Abs(c.Lower-.08065766257979806) > 1e-12 || math.Abs(c.Upper-.4160174322518936) > 1e-12 {
		t.Fatal("Wilson formula drift", c)
	}
	good, err := CompareTaskOutcomes(comparisonFixtureOutcomes(20), comparisonFixturePolicy())
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*ComparisonReport){"status": func(r *ComparisonReport) { r.Status = "no_regression_signal" }, "authority": func(r *ComparisonReport) { r.AdvisoryOnly = false }, "rate": func(r *ComparisonReport) { r.Baseline.Rate = .9 }, "nan": func(r *ComparisonReport) { r.Candidate.Upper = math.NaN() }, "count": func(r *ComparisonReport) { r.Sampled++ }, "accepted": func(r *ComparisonReport) { r.Baseline.Accepted = 21 }, "digest": func(r *ComparisonReport) { r.EvidenceDigest = "bad" }, "method": func(r *ComparisonReport) { r.Method = "causal" }} {
		t.Run(name, func(t *testing.T) {
			bad := good
			mutate(&bad)
			if bad.Validate() == nil {
				t.Fatal("invalid report accepted")
			}
		})
	}
}

func TestComparisonPoliciesRequestsAndLimits(t *testing.T) {
	p := comparisonFixturePolicy()
	for _, source := range []evaluation.Source{evaluation.Deterministic, evaluation.ToolResult, evaluation.LLMJudge} {
		p.Source = source
		if p.Validate() == nil {
			t.Fatal("creative source accepted", source)
		}
	}
	for _, domain := range []string{"code", "coding", "debugging", "math", "structured_json"} {
		p.Execution.Domain = domain
		for _, source := range []evaluation.Source{evaluation.Deterministic, evaluation.ToolResult, evaluation.UserFeedback} {
			p.Source = source
			if p.Validate() != nil {
				t.Fatal(domain, source)
			}
		}
	}
	p = comparisonFixturePolicy()
	for _, drop := range []float64{0, -1, 1.1, math.NaN(), math.Inf(1)} {
		p.MinDrop = drop
		if p.Validate() == nil {
			t.Fatal("drop", drop)
		}
	}
	p = comparisonFixturePolicy()
	request := ComparisonRequest{Version: 1, ModelID: "configured", Domain: p.Execution.Domain, Profile: p.Execution.Profile, Name: p.Key.Name, BaselineVersion: p.BaselineVersion, CandidateVersion: p.CandidateVersion, Source: p.Source, MinSamples: p.MinSamples, MinDrop: p.MinDrop, Tasks: []string{"task"}}
	if request.Validate() != nil {
		t.Fatal("valid request rejected")
	}
	request.Tasks = []string{"task", "task"}
	if request.Validate() == nil {
		t.Fatal("duplicate requested tasks")
	}
	for _, input := range [][]TaskOutcome{nil, comparisonFixtureOutcomes(101)} {
		if _, err := CompareTaskOutcomes(input, p); err == nil {
			t.Fatal("input bounds")
		}
	}
	input := comparisonFixtureOutcomes(100)
	if _, err := CompareTaskOutcomes(input, p); err != nil {
		t.Fatal("exact maximum rejected", err)
	}
}

func TestComparisonConfiguredModelBindingShape(t *testing.T) {
	report, err := CompareTaskOutcomes(comparisonFixtureOutcomes(20), comparisonFixturePolicy())
	if err != nil || report.ConfiguredModelID != "" {
		t.Fatal("pure comparator inferred configuration", err)
	}
	report.ConfiguredModelID = "configured-model"
	if report.Validate() != nil {
		t.Fatal("valid application binding rejected")
	}
	report.ConfiguredModelID = "invalid/model"
	if report.Validate() == nil {
		t.Fatal("invalid configuration identity accepted")
	}
}
