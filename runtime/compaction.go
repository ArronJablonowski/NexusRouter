package runtime

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ContextSummary is untrusted continuation data, never instruction authority.
type ContextSummary struct {
	Requirements []string `json:"requirements,omitempty"`
	Activity     []string `json:"activity,omitempty"`
	Decisions    []string `json:"decisions,omitempty"`
	PendingWork  []string `json:"pending_work,omitempty"`
	Failures     []string `json:"failures,omitempty"`
	Artifacts    []string `json:"artifacts,omitempty"`
}

// ContextCompaction identifies the source snapshot used to shorten context.
// The caller verifies the source digest; the runtime validates its canonical
// representation and persists provenance atomically with the new task input.
type ContextCompaction struct {
	SummaryAttemptID      string         `json:"summary_attempt_id,omitempty"`
	SummaryReviewID       string         `json:"summary_review_id,omitempty"`
	FirstRetainedMessage  int            `json:"first_retained_message,omitempty"`
	FirstRetainedSequence int64          `json:"first_retained_sequence,omitempty"`
	BeforeContextTokens   int            `json:"before_context_tokens,omitempty"`
	AfterContextTokens    int            `json:"after_context_tokens,omitempty"`
	Version               int            `json:"version"`
	SourceTaskID          string         `json:"source_task_id"`
	SourceSequence        int64          `json:"source_sequence"`
	SourceDigest          string         `json:"source_digest"`
	SourceStateDigest     string         `json:"source_state_digest,omitempty"`
	SourceToolCallIDs     []string       `json:"source_tool_call_ids,omitempty"`
	RemovedMessages       int            `json:"removed_messages"`
	Summary               ContextSummary `json:"summary"`
}

func (c ContextCompaction) Validate(parent string) error {
	bad := errors.New("invalid context compaction")
	if (c.SummaryAttemptID == "") != (c.SummaryReviewID == "") {
		return bad
	}
	for _, label := range []string{c.SummaryAttemptID, c.SummaryReviewID} {
		if len(label) > 128 || strings.TrimSpace(label) != label || !utf8.ValidString(label) || strings.ContainsFunc(label, unicode.IsControl) {
			return bad
		}
	}
	if c.FirstRetainedMessage < 0 || c.FirstRetainedSequence < 0 || c.FirstRetainedSequence > c.SourceSequence || c.BeforeContextTokens < 0 || c.AfterContextTokens < 0 || (c.BeforeContextTokens == 0) != (c.AfterContextTokens == 0) {
		return bad
	}
	if (c.Version != 1 && c.Version != 2) || strings.TrimSpace(parent) == "" || c.SourceTaskID != parent || c.SourceSequence < 1 || c.RemovedMessages < 1 || len(c.SourceDigest) != 64 || strings.ToLower(c.SourceDigest) != c.SourceDigest ||
		(c.Version == 1 && (c.SourceStateDigest != "" || len(c.SourceToolCallIDs) != 0)) || (c.Version == 2 && (!validCompactionPlanDigest(c.SourceStateDigest) || !validContextToolCallIDs(c.SourceToolCallIDs))) {
		return bad
	}
	if _, err := hex.DecodeString(c.SourceDigest); err != nil {
		return bad
	}
	entries, bytes := 0, 0
	for _, items := range [][]string{c.Summary.Requirements, c.Summary.Activity, c.Summary.Decisions, c.Summary.PendingWork, c.Summary.Failures, c.Summary.Artifacts} {
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
