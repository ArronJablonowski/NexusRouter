package skills

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/routing"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

// TaskOutcome is an observation, not proof that a referenced skill was followed
// or caused an outcome. Quality and mechanical output checks are independent.
// Nil quality is unknown; nil skill context means legacy/unrecorded attribution.
type TaskOutcome struct {
	Version          int                      `json:"version"`
	TaskID           string                   `json:"task_id"`
	SessionID        string                   `json:"session_id"`
	ParentTaskID     string                   `json:"parent_task_id,omitempty"`
	RetryOfTaskID    string                   `json:"retry_of_task_id,omitempty"`
	State            string                   `json:"state"`
	Privacy          string                   `json:"privacy"`
	Sequence         int64                    `json:"sequence"`
	SkillContext     *runtime.SkillContextUse `json:"skill_context,omitempty"`
	AttemptID        string                   `json:"attempt_id,omitempty"`
	Key              *routing.Key             `json:"key,omitempty"`
	EvaluationID     string                   `json:"evaluation_id,omitempty"`
	EvaluationDigest string                   `json:"evaluation_digest,omitempty"`
	Quality          *evaluation.Outcome      `json:"quality,omitempty"`
	OutputChecks     []TaskOutputCheck        `json:"output_checks"`
}

type TaskOutputCheck struct {
	EventID string `json:"event_id"`
	Code    string `json:"code"`
	Passed  bool   `json:"passed"`
}

func (o TaskOutcome) Validate() error {
	if o.Version != 1 || !sessions.ValidEventPageID(o.TaskID) || !sessions.ValidEventPageID(o.SessionID) || o.Sequence < 1 || o.Sequence > 10000 || o.OutputChecks == nil || len(o.OutputChecks) > 2 {
		return ErrInvalid
	}
	switch o.State {
	case "running", "completed", "failed", "canceled":
	default:
		return ErrInvalid
	}
	if o.Privacy != "" && o.Privacy != "local_only" && o.Privacy != "cloud_allowed" {
		return ErrInvalid
	}
	if o.SkillContext != nil && o.SkillContext.Validate() != nil {
		return ErrInvalid
	}
	for _, id := range []string{o.ParentTaskID, o.RetryOfTaskID} {
		if id != "" && (!sessions.ValidEventPageID(id) || id == o.TaskID) {
			return ErrInvalid
		}
	}
	if (o.AttemptID == "") != (o.Key == nil) {
		return ErrInvalid
	}
	if o.Key != nil {
		if !sessions.ValidEventPageID(o.AttemptID) {
			return ErrInvalid
		}
		for _, value := range []string{o.Key.Model, o.Key.Provider, o.Key.Domain, o.Key.Profile} {
			if value == "" || len(value) > 1024 || !utf8.ValidString(value) || strings.ContainsFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
				return ErrInvalid
			}
		}
	}
	if o.Quality == nil {
		if o.EvaluationID != "" || o.EvaluationDigest != "" {
			return ErrInvalid
		}
	} else {
		if o.Key == nil || !sessions.ValidEventPageID(o.EvaluationID) || len(o.EvaluationDigest) != 64 || strings.ToLower(o.EvaluationDigest) != o.EvaluationDigest {
			return ErrInvalid
		}
		if _, err := hex.DecodeString(o.EvaluationDigest); err != nil {
			return ErrInvalid
		}
		if len(o.Quality.References) < 1 || len(o.Quality.References) > 100 {
			return ErrInvalid
		}
		checks := make([]evaluation.Check, len(o.Quality.References))
		for i, ref := range o.Quality.References {
			if !taskOutcomeReference(ref) {
				return ErrInvalid
			}
			checks[i] = evaluation.Check{Source: o.Quality.Source, Reference: ref, Passed: o.Quality.Accepted}
		}
		if _, err := evaluation.Resolve(checks, true); err != nil {
			return ErrInvalid
		}
	}
	seen := map[string]bool{}
	for _, c := range o.OutputChecks {
		if o.Key == nil || !sessions.ValidEventPageID(c.EventID) || seen[c.Code] || (c.Code != "deterministic.nonempty_text.v1" && c.Code != "deterministic.go_syntax.v1") {
			return ErrInvalid
		}
		seen[c.Code] = true
	}
	body, err := json.Marshal(o)
	if err != nil || len(body) > 64<<10 {
		return ErrInvalid
	}
	return nil
}

// References are trusted opaque IDs: an ASCII alphanumeric prefix followed by
// alphanumerics or _ . : -. This excludes diagnostic prose, paths and escaped
// payloads; it is not a secrecy guarantee. Hosts must still redact/check secrets.
func taskOutcomeReference(ref string) bool {
	if len(ref) < 1 || len(ref) > 128 {
		return false
	}
	for i, c := range []byte(ref) {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			continue
		}
		if i > 0 && (c == '_' || c == '.' || c == ':' || c == '-') {
			continue
		}
		return false
	}
	return true
}
