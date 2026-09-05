package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"time"
)

var selectionModelID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// WorkflowSelection records host-selected, attributable learning inputs. Group
// and Algorithm identify the trusted grouping rule, not proof that arbitrary
// same-domain tasks implement the same procedure. PolicyDigest is an opaque
// host policy binding, not authorization. Sources must be reverified before use.
// ID binds all fields except CreatedAt and can identify a future generation
// attempt. Merely storing this record does not claim or dispatch that attempt.
type WorkflowSelection struct {
	Version      int                 `json:"version"`
	ID           string              `json:"id"`
	Key          Key                 `json:"key"`
	Group        string              `json:"group"`
	Algorithm    string              `json:"algorithm"`
	ModelID      string              `json:"model_id"`
	PolicyDigest string              `json:"policy_digest"`
	Sources      []WorkflowCandidate `json:"sources"`
	CreatedAt    time.Time           `json:"created_at"`
}

// NewWorkflowSelection owns and orders the supplied metadata without rewriting
// provenance. Repeated identical inputs at a different time produce the same ID.
func NewWorkflowSelection(key Key, group, algorithm, modelID, policyDigest string, sources []WorkflowCandidate, createdAt time.Time) (WorkflowSelection, error) {
	if len(sources) < 2 || len(sources) > 20 {
		return WorkflowSelection{}, ErrInvalid
	}
	s := WorkflowSelection{Version: 1, Key: key, Group: group, Algorithm: algorithm, ModelID: modelID, PolicyDigest: policyDigest, Sources: append([]WorkflowCandidate(nil), sources...), CreatedAt: createdAt.UTC()}
	slices.SortFunc(s.Sources, func(a, b WorkflowCandidate) int { return strings.Compare(a.TaskID, b.TaskID) })
	if s.validateMaterial() != nil {
		return WorkflowSelection{}, ErrInvalid
	}
	s.ID = s.identity()
	return s, nil
}

func (s WorkflowSelection) Validate() error {
	if s.validateMaterial() != nil || !selectionDigest(s.ID) || s.ID != s.identity() {
		return ErrInvalid
	}
	return nil
}

func (s WorkflowSelection) validateMaterial() error {
	if s.Version != 1 || !s.Key.valid() || !identifier.MatchString(s.Group) || !identifier.MatchString(s.Algorithm) || !selectionModelID.MatchString(s.ModelID) || !selectionDigest(s.PolicyDigest) || !generationTime(s.CreatedAt) || len(s.Sources) < 2 || len(s.Sources) > 20 {
		return ErrInvalid
	}
	previous, domain := "", s.Sources[0].Domain
	sessions := make(map[string]bool, len(s.Sources))
	for _, source := range s.Sources {
		if source.Validate() != nil || source.TaskID <= previous || source.Domain != domain || sessions[source.SessionID] {
			return ErrInvalid
		}
		previous, sessions[source.SessionID] = source.TaskID, true
	}
	return nil
}

// All fields here are already bounded before serialization. Keep identity
// versioned explicitly: adding new material requires a deliberate contract edit.
func (s WorkflowSelection) identity() string {
	body, _ := json.Marshal(struct {
		Version      int                 `json:"version"`
		Key          Key                 `json:"key"`
		Group        string              `json:"group"`
		Algorithm    string              `json:"algorithm"`
		ModelID      string              `json:"model_id"`
		PolicyDigest string              `json:"policy_digest"`
		Sources      []WorkflowCandidate `json:"sources"`
	}{s.Version, s.Key, s.Group, s.Algorithm, s.ModelID, s.PolicyDigest, s.Sources})
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

func selectionDigest(s string) bool {
	if len(s) != 64 || strings.ToLower(s) != s {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
