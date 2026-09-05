// Package skills stores procedural workflows separately from factual memory.
// Drafts are untrusted content: validation here never grants tool permissions.
package skills

import (
	"context"
	"errors"
	"regexp"
	"time"
)

var (
	ErrInvalid    = errors.New("skills: invalid record")
	ErrNotFound   = errors.New("skills: not found")
	ErrDisabled   = errors.New("skills: automatic mutation disabled")
	ErrValidation = errors.New("skills: validation required")
	ErrConflict   = errors.New("skills: active version changed")
)

var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

type Key struct {
	Scope string `json:"scope"`
	Name  string `json:"name"`
}

func (k Key) valid() bool   { return identifier.MatchString(k.Scope) && identifier.MatchString(k.Name) }
func (k Key) index() string { return k.Scope + "/" + k.Name }

// Draft describes a proposed workflow, not executable trusted instructions.
type Draft struct {
	Key             Key      `json:"key"`
	Description     string   `json:"description"`
	Tags            []string `json:"tags"`
	SourceSessions  []string `json:"source_sessions"`
	SourceEvidence  []string `json:"source_evidence,omitempty"`
	Steps           []string `json:"steps"`
	RequiredTools   []string `json:"required_tools"`
	Configuration   string   `json:"configuration"`
	Risks           []string `json:"risks"`
	ValidationCases []string `json:"validation_cases"`
}

func (d Draft) valid() bool {
	if !d.Key.valid() || d.Description == "" || len(d.Description) > 1024 || len(d.Steps) == 0 || len(d.SourceSessions) == 0 || len(d.ValidationCases) == 0 {
		return false
	}
	for _, list := range [][]string{d.SourceSessions, d.SourceEvidence, d.Tags, d.RequiredTools} {
		for _, value := range list {
			if !identifier.MatchString(value) {
				return false
			}
		}
	}
	for _, list := range [][]string{d.Steps, d.ValidationCases, d.Risks} {
		for _, value := range list {
			if value == "" {
				return false
			}
		}
	}
	return true
}

type Version struct {
	ID        string    `json:"id"`
	Parent    string    `json:"parent,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	Draft     Draft     `json:"draft"`
}

// Metadata is sufficient for progressive discovery; workflow bodies are only
// read by Load. The scope is exact, never a wildcard or directory prefix.
type Metadata struct {
	Key         Key      `json:"key"`
	Version     string   `json:"version"`
	Digest      string   `json:"digest"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
}

// Evidence must originate in a deterministic validator, not an LLM's assertion.
// Trust of the supplied Validator is established by the host application.
type Evidence struct {
	ID            string `json:"id"`
	Passed        bool   `json:"passed"`
	Deterministic bool   `json:"deterministic"`
}
type Validator interface {
	Validate(context.Context, Version) (Evidence, error)
}
type ValidatorFunc func(context.Context, Version) (Evidence, error)

func (f ValidatorFunc) Validate(ctx context.Context, v Version) (Evidence, error) { return f(ctx, v) }

// Store is a trusted procedural-workflow engine. Discover must return only
// active versions accepted by deterministic validation, matching the exact
// scope and at least one requested tag, with at most limit entries. Load must
// return the requested immutable version even if activation changes meanwhile.
// Implementations must honor cancellation and support concurrent calls without
// mutating returned data. Content integrity alone does not prove activation.
type Store interface {
	Discover(context.Context, string, []string, int) ([]Metadata, error)
	Load(context.Context, Key, string) (Version, error)
	Draft(context.Context, Draft, bool) (Version, error)
	Activate(context.Context, Key, string, string, Validator, bool) error
	Rollback(context.Context, Key, string, bool) error
}
