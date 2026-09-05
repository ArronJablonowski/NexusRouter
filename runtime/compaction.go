package runtime

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

// ContextSummary is untrusted continuation data, never instruction authority.
type ContextSummary struct {
	Decisions   []string `json:"decisions,omitempty"`
	PendingWork []string `json:"pending_work,omitempty"`
	Failures    []string `json:"failures,omitempty"`
	Artifacts   []string `json:"artifacts,omitempty"`
}

// ContextCompaction identifies the source snapshot used to shorten context.
// The caller verifies the source digest; the runtime validates its canonical
// representation and persists provenance atomically with the new task input.
type ContextCompaction struct {
	Version         int            `json:"version"`
	SourceTaskID    string         `json:"source_task_id"`
	SourceSequence  int64          `json:"source_sequence"`
	SourceDigest    string         `json:"source_digest"`
	RemovedMessages int            `json:"removed_messages"`
	Summary         ContextSummary `json:"summary"`
}

func (c ContextCompaction) Validate(parent string) error {
	bad := errors.New("invalid context compaction")
	if c.Version != 1 || strings.TrimSpace(parent) == "" || c.SourceTaskID != parent || c.SourceSequence < 1 || c.RemovedMessages < 1 || len(c.SourceDigest) != 64 || strings.ToLower(c.SourceDigest) != c.SourceDigest {
		return bad
	}
	if _, err := hex.DecodeString(c.SourceDigest); err != nil {
		return bad
	}
	entries, bytes := 0, 0
	for _, items := range [][]string{c.Summary.Decisions, c.Summary.PendingWork, c.Summary.Failures, c.Summary.Artifacts} {
		if len(items) > 128 {
			return bad
		}
		for _, item := range items {
			if len(item) > 64<<10 || !utf8.ValidString(item) || strings.TrimSpace(item) == "" {
				return bad
			}
			bytes += len(item)
			if bytes > 64<<10 {
				return bad
			}
			entries++
		}
	}
	if entries == 0 {
		return bad
	}
	body, err := json.Marshal(c.Summary)
	if err != nil || len(body) > 64<<10 {
		return bad
	}
	return nil
}
