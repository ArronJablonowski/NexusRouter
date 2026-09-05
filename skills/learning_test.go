package skills

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
)

func learningExamples() []WorkflowExample {
	return []WorkflowExample{
		{SessionID: "session-b", TaskID: "task-b", Domain: "coding", Steps: []string{"Run focused checks"}, Checks: []evaluation.Check{{Source: evaluation.Deterministic, Reference: "evidence-b", Passed: true}, {Source: evaluation.LLMJudge, Reference: "advisory-b", Passed: true}}},
		{SessionID: "session-a", TaskID: "task-a", Domain: "coding", Steps: []string{"Run focused checks"}, Checks: []evaluation.Check{{Source: evaluation.Deterministic, Reference: "evidence-a", Passed: true}}},
	}
}

func assertNoLearnedVersion(t *testing.T, s *FileStore, key Key) {
	t.Helper()
	h, err := s.History(context.Background(), key)
	if !errors.Is(err, ErrNotFound) || len(h.Versions) != 0 {
		t.Fatal("rejected learning published a draft", h, err)
	}
}

func TestLearningDraftPreservesCanonicalEvidenceWithoutActivation(t *testing.T) {
	path := testPath(t)
	s := openTest(t, path)
	s.SetAutomatic(true)
	examples := learningExamples()
	before, _ := json.Marshal(examples)
	key := sample().Key
	calls := 0
	v, err := s.DraftFromWorkflows(context.Background(), key, examples, DraftGeneratorFunc(func(ctx context.Context, gotKey Key, got []WorkflowExample) (Draft, error) {
		calls++
		if gotKey != key || !reflect.DeepEqual(got, examples) {
			t.Fatal("generator received altered source workflows", gotKey, got)
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 30*time.Second {
			t.Fatal("unbounded draft generator")
		}
		got[0].SessionID = "mutated-session"
		got[0].TaskID = "mutated-task"
		got[0].Domain = "creative"
		got[0].Steps[0] = "mutated step"
		got[0].Checks[0].Reference = "mutated-evidence"
		got[0].Checks[0].Passed = false
		d := sample()
		d.SourceSessions = []string{"forged-session"}
		d.SourceEvidence = []string{"forged-evidence"}
		return d, nil
	}))
	if err != nil || calls != 1 || v.Draft.Key != key || !reflect.DeepEqual(v.Draft.SourceSessions, []string{"session-a", "session-b"}) || !reflect.DeepEqual(v.Draft.SourceEvidence, []string{"evidence-a", "evidence-b"}) {
		t.Fatal(v, err, calls)
	}
	after, _ := json.Marshal(examples)
	if string(before) != string(after) {
		t.Fatal("generator mutated caller inputs")
	}
	items, err := s.Discover(context.Background(), key.Scope, nil, 10)
	if err != nil || len(items) != 0 {
		t.Fatal("generated draft auto-activated", items, err)
	}
	h, err := s.History(context.Background(), key)
	if err != nil || h.Active != "" || len(h.Versions) != 1 {
		t.Fatal(h, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTest(t, path)
	loaded, err := s.Load(context.Background(), key, v.ID)
	if err != nil || !reflect.DeepEqual(loaded.Draft.SourceEvidence, v.Draft.SourceEvidence) || !reflect.DeepEqual(loaded.Draft.SourceSessions, v.Draft.SourceSessions) {
		t.Fatal("learning provenance lost on restart", loaded, err)
	}
}

func TestLearningEvidenceLadderRequiresAcceptedNonJudgeEvidence(t *testing.T) {
	for _, source := range []evaluation.Source{evaluation.Deterministic, evaluation.ToolResult, evaluation.UserFeedback, evaluation.LLMJudge} {
		t.Run(string(source), func(t *testing.T) {
			s := openTest(t, testPath(t))
			s.SetAutomatic(true)
			examples := learningExamples()
			key := sample().Key
			calls := 0
			for i := range examples {
				examples[i].Checks = []evaluation.Check{{Source: source, Reference: fmt.Sprintf("proof-%d", i), Passed: true}}
			}
			v, err := s.DraftFromWorkflows(context.Background(), key, examples, DraftGeneratorFunc(func(context.Context, Key, []WorkflowExample) (Draft, error) { calls++; return sample(), nil }))
			if source == evaluation.LLMJudge {
				if err == nil || calls != 0 || v.ID != "" {
					t.Fatal("judge-only learning admitted", v, err, calls)
				}
				assertNoLearnedVersion(t, s, key)
			} else if err != nil || calls != 1 || v.ID == "" {
				t.Fatal(v, err, calls)
			}
		})
	}
}

func TestLearningInvalidExamplesRejectBeforeGeneration(t *testing.T) {
	for _, kind := range []string{"one", "too_many", "same_session", "same_task", "session_id", "task_id", "domain_mismatch", "domain_invalid", "blank_step", "invalid_utf8", "many_steps", "many_checks", "missing_checks", "failed_objective", "bad_reference"} {
		t.Run(kind, func(t *testing.T) {
			s := openTest(t, testPath(t))
			s.SetAutomatic(true)
			examples := learningExamples()
			key := sample().Key
			switch kind {
			case "one":
				examples = examples[:1]
			case "too_many":
				for len(examples) < 21 {
					n := len(examples)
					examples = append(examples, WorkflowExample{SessionID: fmt.Sprintf("session-%d", n), TaskID: fmt.Sprintf("task-%d", n), Domain: "coding", Steps: []string{"checks"}, Checks: []evaluation.Check{{Source: evaluation.Deterministic, Reference: fmt.Sprintf("proof-%d", n), Passed: true}}})
				}
			case "same_session":
				examples[1].SessionID = examples[0].SessionID
			case "same_task":
				examples[1].TaskID = examples[0].TaskID
			case "session_id":
				examples[0].SessionID = "../bad"
			case "task_id":
				examples[0].TaskID = "bad:task"
			case "domain_mismatch":
				examples[0].Domain = "creative"
			case "domain_invalid":
				examples[0].Domain = "bad domain"
			case "blank_step":
				examples[0].Steps = []string{" \n "}
			case "invalid_utf8":
				examples[0].Steps = []string{string([]byte{255})}
			case "many_steps":
				examples[0].Steps = make([]string, 1001)
				for i := range examples[0].Steps {
					examples[0].Steps[i] = "check"
				}
			case "many_checks":
				examples[0].Checks = make([]evaluation.Check, 1001)
				for i := range examples[0].Checks {
					examples[0].Checks[i] = evaluation.Check{Source: evaluation.Deterministic, Reference: fmt.Sprintf("check-%d", i), Passed: true}
				}
			case "missing_checks":
				examples[0].Checks = nil
			case "failed_objective":
				examples[0].Checks = []evaluation.Check{{Source: evaluation.Deterministic, Reference: "failed-check", Passed: false}, {Source: evaluation.UserFeedback, Reference: "positive-feedback", Passed: true}}
			case "bad_reference":
				examples[0].Checks[0].Reference = "private reference"
			}
			v, err := s.DraftFromWorkflows(context.Background(), key, examples, DraftGeneratorFunc(func(context.Context, Key, []WorkflowExample) (Draft, error) {
				t.Error("invalid workflows reached generator")
				return sample(), nil
			}))
			if err == nil || v.ID != "" {
				t.Fatal(v, err)
			}
			assertNoLearnedVersion(t, s, key)
		})
	}
}

func TestLearningGeneratorFailuresAndDisabledStoreCannotPublish(t *testing.T) {
	for _, kind := range []string{"off", "readonly", "nil", "error", "panic", "cancel_before", "cancel_during", "kill_switch", "wrong_key", "invalid_draft"} {
		t.Run(kind, func(t *testing.T) {
			path := testPath(t)
			s := openTest(t, path)
			s.SetAutomatic(kind != "off")
			key := sample().Key
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if kind == "readonly" {
				seed := sample()
				seed.Key.Name = "unrelated-seed"
				if _, err := s.Draft(context.Background(), seed, false); err != nil {
					t.Fatal(err)
				}
				ro, err := OpenReadOnly(path, []string{"project"})
				if err != nil {
					t.Fatal(err)
				}
				defer ro.Close()
				s = ro
				s.SetAutomatic(true)
			}
			if kind == "cancel_before" {
				cancel()
			}
			generator := DraftGenerator(DraftGeneratorFunc(func(context.Context, Key, []WorkflowExample) (Draft, error) {
				if kind == "off" || kind == "readonly" || kind == "cancel_before" {
					t.Error("disabled generator invoked")
				}
				d := sample()
				switch kind {
				case "error":
					return d, errors.New("private-generator-error")
				case "panic":
					panic("private-generator-error")
				case "cancel_during":
					cancel()
				case "kill_switch":
					s.SetAutomatic(false)
				case "wrong_key":
					d.Key.Name = "different"
				case "invalid_draft":
					d.Steps = nil
				}
				return d, nil
			}))
			if kind == "nil" {
				generator = nil
			}
			v, err := s.DraftFromWorkflows(ctx, key, learningExamples(), generator)
			if err == nil || strings.Contains(err.Error(), "private") || v.ID != "" {
				t.Fatal(v, err)
			}
			assertNoLearnedVersion(t, s, key)
		})
	}
}
