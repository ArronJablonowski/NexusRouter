package evaluation

import (
	"bytes"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/routing"
)

func deprecationRecord(id string, source Source, passed, execution bool) Record {
	return Record{Version: 1, ID: "record-" + id, TaskID: "task-" + id, AttemptID: "attempt", Key: routing.Key{Model: "model", Provider: "provider", Domain: "code", Profile: "default"}, Checks: []Check{{Source: source, Reference: "private-evidence-" + id, Passed: passed}}, AllowJudge: true, ExecutionSucceeded: execution, Time: time.Unix(100, 0)}
}

func TestDeprecationThresholdAndMinimumEvidence(t *testing.T) {
	p := DeprecationPolicy{Window: 5, MinSamples: 2, FailureThreshold: .5}
	records := []Record{deprecationRecord("a", UserFeedback, false, true), deprecationRecord("b", Deterministic, true, true)}
	report, err := SummarizeDeprecation(records, p)
	if err != nil || report.Candidate || report.Reason != "below_threshold" || report.FailureRate != .5 || !report.ApprovalRequired {
		t.Fatal("equal threshold must not cross", report, err)
	}
	records = append(records, deprecationRecord("c", ToolResult, false, true))
	report, err = SummarizeDeprecation(records, p)
	if err != nil || !report.Candidate || report.Reason != "failure_threshold" || report.Failures != 2 || report.EligibleSamples != 3 {
		t.Fatal("crossed threshold missing", report, err)
	}
	p.MinSamples = 4
	report, err = SummarizeDeprecation(records, p)
	if err != nil || report.Candidate || report.Reason != "insufficient_evidence" {
		t.Fatal("sparse evidence triggered recommendation")
	}
	p.MinSamples = 1
	p.FailureThreshold = 1
	report, err = SummarizeDeprecation(records[:1], p)
	if err != nil || report.Candidate {
		t.Fatal("strict crossing of one impossible")
	}
	report, err = SummarizeDeprecation(nil, p)
	if err != nil || report.Sampled != 0 || report.FailureRate != 0 || report.Reason != "insufficient_evidence" || len(report.EvidenceDigest) != 64 {
		t.Fatal("empty window mishandled")
	}
}

func TestDeprecationJudgeExclusionAndFailureUnion(t *testing.T) {
	p := DeprecationPolicy{Window: 10, MinSamples: 1, FailureThreshold: .1}
	for _, passed := range []bool{true, false} {
		report, err := SummarizeDeprecation([]Record{deprecationRecord("judge", LLMJudge, passed, true)}, p)
		if err != nil || report.Candidate || report.EligibleSamples != 0 || report.ExcludedJudgeOnly != 1 {
			t.Fatal("judge alone triggered candidate", report, err)
		}
	}
	records := []Record{deprecationRecord("execution", LLMJudge, true, false), deprecationRecord("both", UserFeedback, false, false), deprecationRecord("schema", UserFeedback, true, true), deprecationRecord("judge-schema", LLMJudge, true, true)}
	failed := false
	records[2].SchemaPassed = &failed
	records[3].SchemaPassed = &failed
	report, err := SummarizeDeprecation(records, p)
	if err != nil || report.ExecutionFailures != 2 || report.QualityFailures != 1 || report.SchemaFailures != 2 || report.Failures != 4 || report.EligibleSamples != 4 || report.ExcludedJudgeOnly != 0 || report.FailureRate != 1 {
		t.Fatal("failure union double-counted or authoritative failure lost", report, err)
	}
	positive := deprecationRecord("precedence", Deterministic, true, true)
	positive.Checks = append(positive.Checks, Check{Source: UserFeedback, Reference: "taste", Passed: false}, Check{Source: LLMJudge, Reference: "judge", Passed: false})
	report, err = SummarizeDeprecation([]Record{positive}, p)
	if err != nil || report.Failures != 0 || report.EligibleSamples != 1 {
		t.Fatal("evidence precedence lost")
	}
}

func TestDeprecationRejectsInvalidEvidenceAndPolicy(t *testing.T) {
	valid := DeprecationPolicy{Window: 2, MinSamples: 1, FailureThreshold: .5}
	for _, p := range []DeprecationPolicy{{0, 1, .5}, {1001, 1, .5}, {2, 0, .5}, {2, 3, .5}, {2, 1, 0}, {2, 1, -1}, {2, 1, 1.1}, {2, 1, math.NaN()}, {2, 1, math.Inf(1)}} {
		if out, err := SummarizeDeprecation(nil, p); err == nil || out.Version != 0 {
			t.Fatal("invalid policy accepted")
		}
	}
	for _, mode := range []string{"duplicate_revision", "key", "invalidrecord", "overflow"} {
		a, b := deprecationRecord("a", Deterministic, true, true), deprecationRecord("b", Deterministic, true, true)
		records := []Record{a, b}
		switch mode {
		case "duplicate_revision":
			records[1].TaskID = a.TaskID
			records[1].AttemptID = a.AttemptID
		case "key":
			records[1].Key.Domain = "creative"
		case "invalidrecord":
			records[1].Cost = math.NaN()
		case "overflow":
			records = append(records, deprecationRecord("c", UserFeedback, true, true))
		}
		if out, err := SummarizeDeprecation(records, valid); err == nil || out.Version != 0 {
			t.Fatal("invalid evidence accepted", mode)
		}
	}
}

func TestDeprecationDigestCanonicalAndNoRawEvidence(t *testing.T) {
	a, b := deprecationRecord("a", UserFeedback, true, true), deprecationRecord("b", ToolResult, false, true)
	a.Checks = append(a.Checks, Check{Source: LLMJudge, Reference: "private-extra", Passed: false})
	records := []Record{b, a}
	before, _ := json.Marshal(records)
	p := DeprecationPolicy{Window: 2, MinSamples: 1, FailureThreshold: .2}
	one, err := SummarizeDeprecation(records, p)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(records)
	if !bytes.Equal(before, after) {
		t.Fatal("input mutated")
	}
	a.Checks = []Check{a.Checks[1], a.Checks[0]}
	two, err := SummarizeDeprecation([]Record{a, b}, p)
	if err != nil || !reflect.DeepEqual(one, two) {
		t.Fatal("ordering changed canonical report")
	}
	body, _ := json.Marshal(one)
	for _, secret := range []string{"private-evidence", "private-extra", "task-a", "record-a", `"AttemptID"`} {
		if strings.Contains(string(body), secret) {
			t.Fatal("raw evidence identity leaked")
		}
	}
	records[0].Checks[0].Passed = true
	three, err := SummarizeDeprecation(records, p)
	if err != nil || one.EvidenceDigest == three.EvidenceDigest {
		t.Fatal("changed evidence did not change digest")
	}
}
