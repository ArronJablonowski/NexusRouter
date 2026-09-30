package evaluation

import (
	"math"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/routing"
)

func TestDeprecationRequestValidation(t *testing.T) {
	valid := DeprecationRequest{Version: 1, ModelID: "model", Domain: "code", Profile: "default", Policy: DeprecationPolicy{Window: 10, MinSamples: 2, FailureThreshold: .35}}
	if valid.Validate() != nil {
		t.Fatal("valid request rejected")
	}
	for _, label := range []string{"", " ", " padded", "padded ", "bad\nkey", "bad\u0085key", strings.Repeat("x", 513), string([]byte{255})} {
		for _, field := range []string{"model", "domain", "profile"} {
			bad := valid
			switch field {
			case "model":
				bad.ModelID = label
			case "domain":
				bad.Domain = label
			case "profile":
				bad.Profile = label
			}
			if bad.Validate() == nil {
				t.Fatal("invalid label accepted", field)
			}
		}
	}
	bad := valid
	bad.Version = 2
	if bad.Validate() == nil {
		t.Fatal("version accepted")
	}
	bad = valid
	bad.Policy.FailureThreshold = math.NaN()
	if bad.Validate() == nil {
		t.Fatal("invalid policy accepted")
	}
}

func TestDeprecationReportValidation(t *testing.T) {
	p := DeprecationPolicy{Window: 4, MinSamples: 2, FailureThreshold: .35}
	valid, err := SummarizeDeprecation([]Record{deprecationRecord("a", UserFeedback, false, false), deprecationRecord("b", Deterministic, true, true)}, p)
	if err != nil || valid.Validate() != nil {
		t.Fatal("summary invalid", err)
	}
	for name, mutate := range map[string]func(*DeprecationReport){
		"version":                  func(r *DeprecationReport) { r.Version = 2 },
		"population":               func(r *DeprecationReport) { r.Population = "all_requests" },
		"approval":                 func(r *DeprecationReport) { r.ApprovalRequired = false },
		"digest":                   func(r *DeprecationReport) { r.EvidenceDigest = strings.Repeat("A", 64) },
		"digest length":            func(r *DeprecationReport) { r.EvidenceDigest = "abc" },
		"sampled":                  func(r *DeprecationReport) { r.Sampled = 5 },
		"negative sampled":         func(r *DeprecationReport) { r.Sampled = -1 },
		"eligible":                 func(r *DeprecationReport) { r.EligibleSamples = 3 },
		"excluded":                 func(r *DeprecationReport) { r.ExcludedJudgeOnly = 1 },
		"negative counter":         func(r *DeprecationReport) { r.QualityFailures = -1 },
		"component greater union":  func(r *DeprecationReport) { r.SchemaFailures = 2 },
		"union greater components": func(r *DeprecationReport) { r.ExecutionFailures = 0; r.QualityFailures = 0 },
		"failure count":            func(r *DeprecationReport) { r.Failures = 3 },
		"rate":                     func(r *DeprecationReport) { r.FailureRate = .7 },
		"nan":                      func(r *DeprecationReport) { r.FailureRate = math.NaN() },
		"infinity":                 func(r *DeprecationReport) { r.FailureRate = math.Inf(1) },
		"candidate":                func(r *DeprecationReport) { r.Candidate = false },
		"reason":                   func(r *DeprecationReport) { r.Reason = "below_threshold" },
		"key":                      func(r *DeprecationReport) { r.Key.Domain = "" },
		"key control":              func(r *DeprecationReport) { r.Key.Model = "bad\n" },
		"configured model":         func(r *DeprecationReport) { r.ConfiguredModelID = "bad " },
		"policy":                   func(r *DeprecationReport) { r.Policy.MinSamples = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			bad := valid
			mutate(&bad)
			if bad.Validate() == nil {
				t.Fatal("forged report accepted")
			}
		})
	}
}

func TestDeprecationReportEmptyAndThresholdValidation(t *testing.T) {
	p := DeprecationPolicy{Window: 4, MinSamples: 2, FailureThreshold: .5}
	empty, err := SummarizeDeprecation(nil, p)
	if err != nil || empty.Validate() != nil {
		t.Fatal("pure empty summary rejected")
	}
	bound := empty
	bound.ConfiguredModelID = "configured"
	if bound.Validate() == nil {
		t.Fatal("configured attribution without key accepted")
	}
	bound.Key = routing.Key{Model: "model", Provider: "provider", Domain: "code", Profile: "default"}
	if bound.Validate() != nil {
		t.Fatal("attributed empty window rejected")
	}
	for _, records := range [][]Record{{deprecationRecord("a", UserFeedback, false, true)}, {deprecationRecord("a", UserFeedback, false, true), deprecationRecord("b", UserFeedback, true, true)}, {deprecationRecord("a", LLMJudge, false, true)}} {
		report, err := SummarizeDeprecation(records, p)
		if err != nil || report.Validate() != nil {
			t.Fatal("valid boundary summary rejected", err)
		}
	}
}
