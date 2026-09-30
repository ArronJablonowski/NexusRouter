package skills

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
)

// WorkflowExample is supplied by a trusted host from completed, attributable
// work. Checks are evaluator records, never claims extracted from model text.
// The host must redact content and enforce privacy before invoking a generator.
type WorkflowExample struct {
	SessionID string             `json:"session_id"`
	TaskID    string             `json:"task_id"`
	Domain    string             `json:"domain"`
	Steps     []string           `json:"steps"`
	Checks    []evaluation.Check `json:"checks"`
}

// DraftGenerator generalizes admitted successful examples into an untrusted
// procedural draft. Generation does not validate or activate that workflow.
type DraftGenerator interface {
	Generate(context.Context, Key, []WorkflowExample) (Draft, error)
}

type DraftGeneratorFunc func(context.Context, Key, []WorkflowExample) (Draft, error)

func (f DraftGeneratorFunc) Generate(ctx context.Context, key Key, examples []WorkflowExample) (Draft, error) {
	return f(ctx, key, examples)
}

func prepareWorkflows(key Key, examples []WorkflowExample) ([]WorkflowExample, []string, []string, error) {
	if !key.valid() || len(examples) < 2 || len(examples) > 20 {
		return nil, nil, nil, ErrInvalid
	}
	budget := 256 << 10
	sessions, tasks, evidence := map[string]bool{}, map[string]bool{}, map[string]bool{}
	domain := examples[0].Domain
	owned := make([]WorkflowExample, len(examples))
	for i, example := range examples {
		if !identifier.MatchString(example.SessionID) || !identifier.MatchString(example.TaskID) || !identifier.MatchString(example.Domain) || example.Domain != domain || sessions[example.SessionID] || tasks[example.TaskID] || len(example.Steps) < 1 || len(example.Steps) > 128 || len(example.Checks) < 1 || len(example.Checks) > 100 {
			return nil, nil, nil, ErrInvalid
		}
		if !contextStrings(&budget, []string{example.SessionID, example.TaskID, example.Domain}) || !contextStrings(&budget, example.Steps) {
			return nil, nil, nil, ErrInvalid
		}
		for _, step := range example.Steps {
			if strings.TrimSpace(step) == "" {
				return nil, nil, nil, ErrInvalid
			}
		}
		for _, check := range example.Checks {
			if !identifier.MatchString(check.Reference) || !contextStrings(&budget, []string{string(check.Source), check.Reference}) {
				return nil, nil, nil, ErrInvalid
			}
		}
		outcome, err := evaluation.Resolve(example.Checks, false)
		if err != nil || !outcome.Accepted {
			return nil, nil, nil, ErrValidation
		}
		for _, ref := range outcome.References {
			evidence[ref] = true
		}
		sessions[example.SessionID], tasks[example.TaskID] = true, true
		owned[i] = example
		owned[i].Steps = slices.Clone(example.Steps)
		owned[i].Checks = slices.Clone(example.Checks)
	}
	encoded, err := json.Marshal(owned)
	if err != nil || len(encoded) > 256<<10 {
		return nil, nil, nil, ErrInvalid
	}
	return owned, sortedKeys(sessions), sortedKeys(evidence), nil
}

func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func validateGeneratedDraft(d Draft) error {
	budget := 256 << 10
	if !contextStrings(&budget, []string{d.Key.Scope, d.Key.Name, d.Description, d.Configuration}) {
		return ErrInvalid
	}
	for _, values := range [][]string{d.Tags, d.SourceSessions, d.SourceEvidence, d.Steps, d.RequiredTools, d.Risks, d.ValidationCases} {
		if !contextStrings(&budget, values) {
			return ErrInvalid
		}
	}
	if !d.valid() || strings.TrimSpace(d.Description) == "" {
		return ErrInvalid
	}
	for _, values := range [][]string{d.Steps, d.ValidationCases} {
		for _, value := range values {
			if strings.TrimSpace(value) == "" {
				return ErrInvalid
			}
		}
	}
	body, err := json.Marshal(d)
	if err != nil || len(body) > 256<<10 {
		return ErrInvalid
	}
	return nil
}
