package skills

import (
	"slices"
	"strings"
)

// WorkflowBucket retains a bounded exact-sequence observation across scan pages.
// A singleton is not yet a group; neither form is acceptance evidence or tool authority.
type WorkflowBucket WorkflowGroup

func NewWorkflowBucket(p WorkflowProcedure) (WorkflowBucket, error) {
	if p.Validate() != nil || len(p.Tools) == 0 {
		return WorkflowBucket{}, ErrInvalid
	}
	g := WorkflowGroup{Version: 1, Algorithm: ObservedToolsAlgorithm, Domain: p.Candidate.Domain, Profile: p.Profile, Tools: slices.Clone(p.Tools), Sources: []WorkflowCandidate{p.Candidate}}
	g.ID = g.identity()
	return WorkflowBucket(g), nil
}

func (b WorkflowBucket) Validate() error {
	g := WorkflowGroup(b)
	if g.Version != 1 || g.Algorithm != ObservedToolsAlgorithm || !identifier.MatchString(g.Domain) || !identifier.MatchString(g.Profile) || !validWorkflowTools(g.Tools, 1) || len(g.Sources) < 1 || len(g.Sources) > 20 || g.ID != g.identity() {
		return ErrInvalid
	}
	previous := ""
	sessions := map[string]bool{}
	for _, source := range b.Sources {
		if source.Validate() != nil || source.Domain != b.Domain || source.TaskID <= previous || sessions[source.SessionID] {
			return ErrInvalid
		}
		previous = source.TaskID
		sessions[source.SessionID] = true
	}
	return nil
}

// Merge owns its output, chooses the lexical first task per session, and keeps
// the lexical first twenty sessions. Conflicting retained task metadata fails closed.
func (b WorkflowBucket) Merge(p WorkflowProcedure) (WorkflowBucket, error) {
	if b.Validate() != nil || p.Validate() != nil || p.Candidate.Domain != b.Domain || p.Profile != b.Profile || !slices.Equal(p.Tools, b.Tools) {
		return WorkflowBucket{}, ErrInvalid
	}
	out := b
	out.Tools = slices.Clone(b.Tools)
	out.Sources = slices.Clone(b.Sources)
	for _, source := range out.Sources {
		if source.TaskID == p.Candidate.TaskID {
			if source != p.Candidate {
				return WorkflowBucket{}, ErrInvalid
			}
			return out, nil
		}
	}
	out.Sources = append(out.Sources, p.Candidate)
	slices.SortFunc(out.Sources, func(a, b WorkflowCandidate) int { return strings.Compare(a.TaskID, b.TaskID) })
	seen := map[string]bool{}
	retained := make([]WorkflowCandidate, 0, 20)
	for _, source := range out.Sources {
		if !seen[source.SessionID] && len(retained) < 20 {
			retained = append(retained, source)
			seen[source.SessionID] = true
		}
	}
	out.Sources = retained
	return out, out.Validate()
}

// Group returns an owned mature group; a singleton cannot produce a draft selection.
func (b WorkflowBucket) Group() (WorkflowGroup, error) {
	if b.Validate() != nil || len(b.Sources) < 2 {
		return WorkflowGroup{}, ErrInvalid
	}
	g := WorkflowGroup(b)
	g.Tools = slices.Clone(b.Tools)
	g.Sources = slices.Clone(b.Sources)
	return g, g.Validate()
}
