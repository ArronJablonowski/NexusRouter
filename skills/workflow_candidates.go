package skills

import (
	"encoding/hex"
	"strings"

	"github.com/ArronJablonowski/NexusRouter/sessions"
)

// WorkflowCandidate is metadata for an observed accepted learning source. It
// contains neither conversation content nor executable steps. Selection is not
// a promise that feedback or task state will remain unchanged before generation.
type WorkflowCandidate struct {
	TaskID           string `json:"task_id"`
	SessionID        string `json:"session_id"`
	Domain           string `json:"domain"`
	Privacy          string `json:"privacy"`
	EvaluationID     string `json:"evaluation_id"`
	EvaluationDigest string `json:"evaluation_digest"`
	SourceDigest     string `json:"source_digest"`
	SourceSequence   int64  `json:"source_sequence"`
}

func (c WorkflowCandidate) Validate() error {
	if !identifier.MatchString(c.TaskID) || !identifier.MatchString(c.SessionID) || !identifier.MatchString(c.Domain) || (c.Privacy != "local_only" && c.Privacy != "cloud_allowed") || !sessions.ValidEventPageID(c.EvaluationID) || c.SourceSequence < 1 || c.SourceSequence > 10000 {
		return ErrInvalid
	}
	for _, digest := range []string{c.EvaluationDigest, c.SourceDigest} {
		if len(digest) != 64 || strings.ToLower(digest) != digest {
			return ErrInvalid
		}
		if _, err := hex.DecodeString(digest); err != nil {
			return ErrInvalid
		}
	}
	return nil
}

// WorkflowCandidatePage advances over a bounded window of task IDs, including
// ineligible tasks. A full window returns its last scanned ID in Next, even if
// it found no candidates; a following page can be empty. Pages are live views,
// not a frozen cross-page snapshot. Callers still need distinct source sessions.
type WorkflowCandidatePage struct {
	Version    int                 `json:"version"`
	Domain     string              `json:"domain"`
	Candidates []WorkflowCandidate `json:"candidates"`
	Scanned    int                 `json:"scanned"`
	Next       string              `json:"next"`
}

func (p WorkflowCandidatePage) Validate(after string, scanLimit int) error {
	if p.Version != 1 || !identifier.MatchString(p.Domain) || (after != "" && !sessions.ValidEventPageID(after)) || scanLimit < 1 || scanLimit > 20 || p.Scanned < 0 || p.Scanned > scanLimit || p.Candidates == nil || len(p.Candidates) > p.Scanned {
		return ErrInvalid
	}
	if p.Scanned == scanLimit {
		if !sessions.ValidEventPageID(p.Next) || p.Next <= after {
			return ErrInvalid
		}
	} else if p.Next != "" {
		return ErrInvalid
	}
	previous := after
	for _, c := range p.Candidates {
		if c.Validate() != nil || c.Domain != p.Domain || c.TaskID <= previous || (p.Next != "" && c.TaskID > p.Next) {
			return ErrInvalid
		}
		previous = c.TaskID
	}
	return nil
}
