package evaluation

import (
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/routing"
)

func TestEvidencePrecedence(t *testing.T) {
	checks := []Check{{LLMJudge, "judge", true}, {UserFeedback, "feedback", true}, {Deterministic, "test", false}, {Deterministic, "schema", true}}
	out, err := Resolve(checks, true)
	if err != nil || out.Accepted || out.Source != Deterministic || len(out.References) != 2 {
		t.Fatalf("%+v %v", out, err)
	}
	for _, c := range [][]Check{nil, {{LLMJudge, "judge", true}}, {{Source("self_rating"), "model", true}}, {{Deterministic, "same", true}, {ToolResult, "same", true}}} {
		if _, err := Resolve(c, false); err == nil {
			t.Fatal("unsupported evidence accepted")
		}
	}
}

func TestObjectiveTestToolAndSchemaEvidence(t *testing.T) {
	for _, tc := range []struct {
		check Check
	}{
		{Check{Source: Deterministic, Reference: "test-suite-receipt", Passed: true}},
		{Check{Source: ToolResult, Reference: "tool-call-receipt", Passed: false}},
	} {
		out, err := Resolve([]Check{tc.check}, false)
		if err != nil || out.Source != tc.check.Source || out.Accepted != tc.check.Passed || len(out.References) != 1 || out.References[0] != tc.check.Reference {
			t.Fatalf("%+v %v", out, err)
		}
	}
	passed := true
	record := Record{Version: 1, ID: "evaluation", TaskID: "task", AttemptID: "attempt", Key: routing.Key{Model: "model", Provider: "provider", Domain: "code", Profile: "default"}, Checks: []Check{{Source: Deterministic, Reference: "test-suite-receipt", Passed: true}, {Source: ToolResult, Reference: "tool-call-receipt", Passed: true}}, SchemaPassed: &passed, ExecutionSucceeded: true, Time: time.Unix(100, 0).UTC()}
	if err := record.Validate(); err != nil {
		t.Fatal("objective evidence record rejected", err)
	}
}
