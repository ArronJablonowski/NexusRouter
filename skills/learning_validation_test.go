package skills

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
)

func validWorkflowExamples() []WorkflowExample {
	return []WorkflowExample{
		{SessionID: "session-z", TaskID: "task-z", Domain: "code", Steps: []string{"Run tests", "Inspect result"}, Checks: []evaluation.Check{{Source: evaluation.Deterministic, Reference: "test-z", Passed: true}}},
		{SessionID: "session-a", TaskID: "task-a", Domain: "code", Steps: []string{"Run tests"}, Checks: []evaluation.Check{{Source: evaluation.UserFeedback, Reference: "feedback-a", Passed: true}, {Source: evaluation.LLMJudge, Reference: "judge-ignored", Passed: false}}},
	}
}

func TestPrepareWorkflowsCanonicalProvenanceAndIsolation(t *testing.T) {
	examples := validWorkflowExamples()
	got, sessions, evidence, err := prepareWorkflows(Key{Scope: "project", Name: "checks"}, examples)
	if err != nil || !reflect.DeepEqual(got, examples) || !reflect.DeepEqual(sessions, []string{"session-a", "session-z"}) || !reflect.DeepEqual(evidence, []string{"feedback-a", "test-z"}) {
		t.Fatal(got, sessions, evidence, err)
	}
	got[0].Steps[0] = "changed"
	got[0].Checks[0].Reference = "changed"
	got[0].SessionID = "changed"
	if examples[0].Steps[0] != "Run tests" || examples[0].Checks[0].Reference != "test-z" || examples[0].SessionID != "session-z" {
		t.Fatal("prepared examples alias input")
	}
	examples[1].Steps[0] = "input changed"
	if got[1].Steps[0] == "input changed" {
		t.Fatal("input aliases prepared workflow")
	}
	examples = validWorkflowExamples()
	examples[1].Checks = []evaluation.Check{{Source: evaluation.ToolResult, Reference: "test-z", Passed: true}}
	_, _, evidence, err = prepareWorkflows(Key{Scope: "project", Name: "checks"}, examples)
	if err != nil || !reflect.DeepEqual(evidence, []string{"test-z"}) {
		t.Fatal("cross-workflow provenance not deduplicated", evidence, err)
	}
}

func TestPrepareWorkflowsRejectsInvalidBoundariesAndEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Key, *[]WorkflowExample)
	}{
		{"key", func(k *Key, _ *[]WorkflowExample) { k.Scope = "*" }},
		{"none", func(_ *Key, x *[]WorkflowExample) { *x = nil }},
		{"one", func(_ *Key, x *[]WorkflowExample) { *x = (*x)[:1] }},
		{"many", func(_ *Key, x *[]WorkflowExample) {
			*x = make([]WorkflowExample, 21)
			for i := range *x {
				(*x)[i] = WorkflowExample{SessionID: fmt.Sprintf("session-%d", i), TaskID: fmt.Sprintf("task-%d", i), Domain: "code", Steps: []string{"check"}, Checks: []evaluation.Check{{Source: evaluation.Deterministic, Reference: "check", Passed: true}}}
			}
		}},
		{"duplicate session", func(_ *Key, x *[]WorkflowExample) { (*x)[1].SessionID = (*x)[0].SessionID }},
		{"duplicate task", func(_ *Key, x *[]WorkflowExample) { (*x)[1].TaskID = (*x)[0].TaskID }},
		{"bad session", func(_ *Key, x *[]WorkflowExample) { (*x)[0].SessionID = "bad/session" }},
		{"long session", func(_ *Key, x *[]WorkflowExample) { (*x)[0].SessionID = strings.Repeat("a", 65) }},
		{"bad task", func(_ *Key, x *[]WorkflowExample) { (*x)[0].TaskID = "bad task" }},
		{"long task", func(_ *Key, x *[]WorkflowExample) { (*x)[0].TaskID = strings.Repeat("a", 65) }},
		{"different domain", func(_ *Key, x *[]WorkflowExample) { (*x)[1].Domain = "creative" }},
		{"empty domain", func(_ *Key, x *[]WorkflowExample) { (*x)[0].Domain = "" }},
		{"no steps", func(_ *Key, x *[]WorkflowExample) { (*x)[0].Steps = nil }},
		{"too many steps", func(_ *Key, x *[]WorkflowExample) {
			(*x)[0].Steps = make([]string, 129)
			for i := range (*x)[0].Steps {
				(*x)[0].Steps[i] = "check"
			}
		}},
		{"blank step", func(_ *Key, x *[]WorkflowExample) { (*x)[0].Steps[0] = " \t\n" }},
		{"step UTF8", func(_ *Key, x *[]WorkflowExample) { (*x)[0].Steps[0] = string([]byte{255}) }},
		{"input budget", func(_ *Key, x *[]WorkflowExample) { (*x)[0].Steps[0] = strings.Repeat("x", 256<<10) }},
		{"no checks", func(_ *Key, x *[]WorkflowExample) { (*x)[0].Checks = nil }},
		{"too many checks", func(_ *Key, x *[]WorkflowExample) {
			(*x)[0].Checks = make([]evaluation.Check, 101)
			for i := range (*x)[0].Checks {
				(*x)[0].Checks[i] = evaluation.Check{Source: evaluation.Deterministic, Reference: fmt.Sprintf("check-%d", i), Passed: true}
			}
		}},
		{"duplicate check", func(_ *Key, x *[]WorkflowExample) { (*x)[0].Checks = append((*x)[0].Checks, (*x)[0].Checks[0]) }},
		{"invalid check ID", func(_ *Key, x *[]WorkflowExample) { (*x)[0].Checks[0].Reference = "bad:id" }},
		{"long check ID", func(_ *Key, x *[]WorkflowExample) { (*x)[0].Checks[0].Reference = strings.Repeat("a", 65) }},
		{"judge only", func(_ *Key, x *[]WorkflowExample) {
			(*x)[0].Checks = []evaluation.Check{{Source: evaluation.LLMJudge, Reference: "judge", Passed: true}}
		}},
		{"unknown source", func(_ *Key, x *[]WorkflowExample) { (*x)[0].Checks[0].Source = "model_self_assessment" }},
		{"deterministic failure", func(_ *Key, x *[]WorkflowExample) {
			(*x)[0].Checks = []evaluation.Check{{Source: evaluation.Deterministic, Reference: "tests", Passed: false}, {Source: evaluation.UserFeedback, Reference: "user", Passed: true}}
		}},
		{"tool failure", func(_ *Key, x *[]WorkflowExample) {
			(*x)[0].Checks = []evaluation.Check{{Source: evaluation.ToolResult, Reference: "tool", Passed: false}, {Source: evaluation.UserFeedback, Reference: "user", Passed: true}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := Key{Scope: "project", Name: "checks"}
			x := validWorkflowExamples()
			tc.mutate(&key, &x)
			if _, _, _, err := prepareWorkflows(key, x); err == nil {
				t.Fatal("invalid workflows admitted")
			}
		})
	}
	maximal := make([]WorkflowExample, 20)
	for i := range maximal {
		maximal[i] = WorkflowExample{SessionID: fmt.Sprintf("session-%02d", i), TaskID: fmt.Sprintf("task-%02d", i), Domain: "code", Steps: []string{"check"}, Checks: []evaluation.Check{{Source: evaluation.Deterministic, Reference: "shared-check", Passed: true}}}
	}
	if _, sessions, _, err := prepareWorkflows(Key{Scope: "project", Name: "checks"}, maximal); err != nil || len(sessions) != 20 {
		t.Fatal("20 valid workflows rejected", err)
	}
	boundary := validWorkflowExamples()
	boundary[0].Steps = make([]string, 128)
	for i := range boundary[0].Steps {
		boundary[0].Steps[i] = "check"
	}
	boundary[0].Checks = make([]evaluation.Check, 100)
	for i := range boundary[0].Checks {
		boundary[0].Checks[i] = evaluation.Check{Source: evaluation.Deterministic, Reference: fmt.Sprintf("check-%03d", i), Passed: true}
	}
	if _, _, _, err := prepareWorkflows(Key{Scope: "project", Name: "checks"}, boundary); err != nil {
		t.Fatal("exact step/check bounds rejected", err)
	}
}

func TestValidateGeneratedDraftBoundaries(t *testing.T) {
	if err := validateGeneratedDraft(sample()); err != nil {
		t.Fatal("valid draft rejected", err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*Draft)
	}{
		{"key", func(d *Draft) { d.Key.Scope = "*" }},
		{"blank description", func(d *Draft) { d.Description = " \t" }},
		{"description UTF8", func(d *Draft) { d.Description = string([]byte{255}) }},
		{"long description", func(d *Draft) { d.Description = strings.Repeat("x", 1025) }},
		{"blank step", func(d *Draft) { d.Steps = []string{" \n"} }},
		{"step UTF8", func(d *Draft) { d.Steps = []string{string([]byte{255})} }},
		{"blank case", func(d *Draft) { d.ValidationCases = []string{" \n"} }},
		{"case UTF8", func(d *Draft) { d.ValidationCases = []string{string([]byte{255})} }},
		{"configuration UTF8", func(d *Draft) { d.Configuration = string([]byte{255}) }},
		{"risk UTF8", func(d *Draft) { d.Risks = []string{string([]byte{255})} }},
		{"budget", func(d *Draft) { d.Configuration = strings.Repeat("x", 256<<10) }},
		{"bad evidence", func(d *Draft) { d.SourceEvidence = []string{"bad:evidence"} }},
		{"long evidence", func(d *Draft) { d.SourceEvidence = []string{strings.Repeat("a", 65)} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := sample()
			tc.mutate(&d)
			if err := validateGeneratedDraft(d); err == nil {
				t.Fatal("invalid generated draft admitted")
			}
		})
	}
	for _, field := range []string{"tags", "sessions", "steps", "tools", "risks", "cases", "evidence"} {
		t.Run("list_"+field, func(t *testing.T) {
			d := sample()
			values := make([]string, 4097)
			for i := range values {
				values[i] = "value"
			}
			switch field {
			case "tags":
				d.Tags = values
			case "sessions":
				d.SourceSessions = values
			case "steps":
				d.Steps = values
			case "tools":
				d.RequiredTools = values
			case "risks":
				d.Risks = values
			case "cases":
				d.ValidationCases = values
			case "evidence":
				d.SourceEvidence = values
			}
			if err := validateGeneratedDraft(d); err == nil {
				t.Fatal("unbounded generated list admitted")
			}
		})
	}
}
