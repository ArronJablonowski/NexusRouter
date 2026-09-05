package skills

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func selectionFixture(t *testing.T) WorkflowSelection {
	t.Helper()
	a, b := candidateFixture(), candidateFixture()
	a.TaskID, a.SessionID = "task-a", "session-a"
	b.TaskID, b.SessionID = "task-b", "session-b"
	s, err := NewWorkflowSelection(Key{Scope: "project", Name: "workflow"}, "review", "tool_sequence_v1", "generator.alias", strings.Repeat("c", 64), []WorkflowCandidate{b, a}, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestWorkflowSelectionStableIdentityAndOwnedSources(t *testing.T) {
	s := selectionFixture(t)
	if s.ID != "6c090393bf34b6a1de68766b5c6c0d6b179dd722c6f0b4528f6985134875e3ab" {
		t.Fatal("version 1 identity encoding changed")
	}
	if s.Validate() != nil || len(s.ID) != 64 || s.Sources[0].TaskID != "task-a" {
		t.Fatal("invalid constructed selection")
	}
	inputs := []WorkflowCandidate{s.Sources[1], s.Sources[0]}
	before := append([]WorkflowCandidate{}, inputs...)
	other, err := NewWorkflowSelection(s.Key, s.Group, s.Algorithm, s.ModelID, s.PolicyDigest, inputs, s.CreatedAt.Add(time.Hour))
	if err != nil || other.ID != s.ID || !reflect.DeepEqual(inputs, before) {
		t.Fatal("timestamp/order changed identity or input", err)
	}
	inputs[0].Privacy = "cloud_allowed"
	if !reflect.DeepEqual(other.Sources, s.Sources) {
		t.Fatal("constructor retained input slice")
	}
}

func TestWorkflowSelectionBindsMaterialAndRejectsMalformedRecords(t *testing.T) {
	for name, mutate := range map[string]func(*WorkflowSelection){
		"scope":             func(s *WorkflowSelection) { s.Key.Scope = "other" },
		"name":              func(s *WorkflowSelection) { s.Key.Name = "other" },
		"group":             func(s *WorkflowSelection) { s.Group = "other" },
		"algorithm":         func(s *WorkflowSelection) { s.Algorithm = "v2" },
		"model":             func(s *WorkflowSelection) { s.ModelID = "other-model" },
		"policy":            func(s *WorkflowSelection) { s.PolicyDigest = strings.Repeat("d", 64) },
		"source":            func(s *WorkflowSelection) { s.Sources[0].SourceDigest = strings.Repeat("e", 64) },
		"evaluation":        func(s *WorkflowSelection) { s.Sources[0].EvaluationID = "new-evidence" },
		"evaluation-digest": func(s *WorkflowSelection) { s.Sources[0].EvaluationDigest = strings.Repeat("f", 64) },
		"sequence":          func(s *WorkflowSelection) { s.Sources[0].SourceSequence++ },
		"privacy":           func(s *WorkflowSelection) { s.Sources[0].Privacy = "cloud_allowed" },
		"order":             func(s *WorkflowSelection) { s.Sources[0], s.Sources[1] = s.Sources[1], s.Sources[0] },
		"duplicate-task":    func(s *WorkflowSelection) { s.Sources[1].TaskID = s.Sources[0].TaskID },
		"duplicate-session": func(s *WorkflowSelection) { s.Sources[1].SessionID = s.Sources[0].SessionID },
		"domain":            func(s *WorkflowSelection) { s.Sources[1].Domain = "code" },
		"empty":             func(s *WorkflowSelection) { s.Sources = nil },
		"single":            func(s *WorkflowSelection) { s.Sources = s.Sources[:1] },
		"version":           func(s *WorkflowSelection) { s.Version = 2 },
		"time":              func(s *WorkflowSelection) { s.CreatedAt = time.Time{} },
		"id":                func(s *WorkflowSelection) { s.ID = strings.Repeat("a", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			s := selectionFixture(t)
			mutate(&s)
			if s.Validate() == nil {
				t.Fatal("changed selection accepted under old ID")
			}
		})
	}
}
