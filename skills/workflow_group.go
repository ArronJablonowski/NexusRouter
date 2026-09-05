package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
)

const ObservedToolsAlgorithm = "observed_tools_v1"

var workflowToolName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// WorkflowProcedure contains observed tool names only, never arguments, results,
// or instructions. Observations do not establish semantic workflow equivalence.
type WorkflowProcedure struct {
	Version   int               `json:"version"`
	Candidate WorkflowCandidate `json:"candidate"`
	Profile   string            `json:"profile"`
	Tools     []string          `json:"tools"`
}

func (p WorkflowProcedure) Validate() error {
	if p.Version != 1 || p.Candidate.Validate() != nil || !identifier.MatchString(p.Profile) || !validWorkflowTools(p.Tools, 0) {
		return ErrInvalid
	}
	return nil
}

// WorkflowGroup is an exact observed-sequence heuristic for inactive drafts,
// not acceptance evidence or permission to execute its tools. Its ID describes
// the grouping rule; a subsequent selection must bind the precise sources.
type WorkflowGroup struct {
	Version   int                 `json:"version"`
	ID        string              `json:"id"`
	Algorithm string              `json:"algorithm"`
	Domain    string              `json:"domain"`
	Profile   string              `json:"profile"`
	Tools     []string            `json:"tools"`
	Sources   []WorkflowCandidate `json:"sources"`
}

func (g WorkflowGroup) Validate() error {
	if g.Version != 1 || g.Algorithm != ObservedToolsAlgorithm || !identifier.MatchString(g.Domain) || !identifier.MatchString(g.Profile) || !validWorkflowTools(g.Tools, 1) || len(g.Sources) < 2 || len(g.Sources) > 20 || g.ID != g.identity() {
		return ErrInvalid
	}
	sessions := map[string]bool{}
	previous := ""
	for _, source := range g.Sources {
		if source.Validate() != nil || source.Domain != g.Domain || source.TaskID <= previous || sessions[source.SessionID] {
			return ErrInvalid
		}
		previous = source.TaskID
		sessions[source.SessionID] = true
	}
	return nil
}

func validWorkflowTools(tools []string, min int) bool {
	if tools == nil || len(tools) < min || len(tools) > 64 {
		return false
	}
	for _, tool := range tools {
		if !workflowToolName.MatchString(tool) {
			return false
		}
	}
	return true
}

func (g WorkflowGroup) identity() string {
	body, _ := json.Marshal(struct {
		Version   int      `json:"version"`
		Algorithm string   `json:"algorithm"`
		Domain    string   `json:"domain"`
		Profile   string   `json:"profile"`
		Tools     []string `json:"tools"`
	}{g.Version, g.Algorithm, g.Domain, g.Profile, g.Tools})
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

// BuildWorkflowGroups owns all returned slices. Within each exact grouping,
// multiple tasks from one session count once: the lexically first task wins.
func BuildWorkflowGroups(procedures []WorkflowProcedure) ([]WorkflowGroup, error) {
	if len(procedures) < 1 || len(procedures) > 20 {
		return nil, ErrInvalid
	}
	seen := map[string]bool{}
	ordered := append([]WorkflowProcedure(nil), procedures...)
	for _, p := range ordered {
		if p.Validate() != nil || seen[p.Candidate.TaskID] {
			return nil, ErrInvalid
		}
		seen[p.Candidate.TaskID] = true
	}
	slices.SortFunc(ordered, func(a, b WorkflowProcedure) int { return strings.Compare(a.Candidate.TaskID, b.Candidate.TaskID) })
	groups := map[string]*WorkflowGroup{}
	for _, p := range ordered {
		if len(p.Tools) == 0 {
			continue
		}
		g := WorkflowGroup{Version: 1, Algorithm: ObservedToolsAlgorithm, Domain: p.Candidate.Domain, Profile: p.Profile, Tools: append([]string{}, p.Tools...), Sources: []WorkflowCandidate{}}
		g.ID = g.identity()
		current := groups[g.ID]
		if current == nil {
			current = &g
			groups[g.ID] = current
		}
		duplicateSession := false
		for _, s := range current.Sources {
			if s.SessionID == p.Candidate.SessionID {
				duplicateSession = true
				break
			}
		}
		if !duplicateSession {
			current.Sources = append(current.Sources, p.Candidate)
		}
	}
	out := []WorkflowGroup{}
	for _, g := range groups {
		if len(g.Sources) < 2 {
			continue
		}
		if g.Validate() != nil {
			return nil, ErrInvalid
		}
		out = append(out, *g)
	}
	slices.SortFunc(out, func(a, b WorkflowGroup) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}
