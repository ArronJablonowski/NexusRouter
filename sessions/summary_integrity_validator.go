package sessions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/ArronJablonowski/NexusRouter/providers"
)

// SummaryIntegrityValidatorID changes whenever the validator's deterministic
// semantics change. Hosts must still select it explicitly.
const SummaryIntegrityValidatorID = "darwin.summary.integrity.v1"

var (
	summaryIssueAnchor = regexp.MustCompile(`(?i)\b[a-z][a-z0-9]{1,9}-[1-9][0-9]{0,8}\b`)
	summaryHexAnchor   = regexp.MustCompile(`(?i)\b[0-9a-f]{12,64}\b`)
	summaryURLAnchor   = regexp.MustCompile(`(?i)https?://[^\s<>"]+`)
)

// SummaryIntegrityValidator is a conservative stock linter. It can reject a
// mechanically demonstrated integrity defect, but always abstains otherwise.
// In particular, it never turns lexical support into semantic approval.
type SummaryIntegrityValidator struct{}

var _ SummaryValidator = SummaryIntegrityValidator{}

func NewSummaryIntegrityValidator() SummaryValidator { return SummaryIntegrityValidator{} }

func (SummaryIntegrityValidator) ValidateSummary(ctx context.Context, in SummaryValidationInput) (SummaryValidationDecision, error) {
	if ctx == nil {
		return SummaryValidationDecision{}, ErrHistory
	}
	if err := ctx.Err(); err != nil {
		return SummaryValidationDecision{}, err
	}
	removed, defect := validateSummaryIntegrityBinding(in)
	if defect != "" {
		return integrityDecision("rejected", defect, 0, 0), nil
	}
	entries := summaryEntries(in.Draft.Request.Summary)
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return SummaryValidationDecision{}, err
		}
		if containsUnsafeSummaryDisplay(entry) {
			return integrityDecision("rejected", "unsafe_display", len(entries), removed), nil
		}
		key := strings.ToLower(strings.Join(strings.Fields(entry), " "))
		if _, exists := seen[key]; exists {
			return integrityDecision("rejected", "duplicate_entry", len(entries), removed), nil
		}
		seen[key] = struct{}{}
	}
	sourceAnchors := make(map[string]struct{})
	for _, message := range in.Source.Messages {
		if err := ctx.Err(); err != nil {
			return SummaryValidationDecision{}, err
		}
		collectMessageAnchors(sourceAnchors, message)
	}
	for _, entry := range entries {
		anchors, oversized := extractSummaryAnchors(entry)
		if oversized {
			return integrityDecision("rejected", "oversized_anchor", len(entries), removed), nil
		}
		for _, anchor := range anchors {
			if _, ok := sourceAnchors[anchor]; !ok {
				return integrityDecision("rejected", "unsupported_anchor", len(entries), removed), nil
			}
		}
	}
	return integrityDecision("abstained", "semantic_review_required", len(entries), removed), nil
}

func validateSummaryIntegrityBinding(in SummaryValidationInput) (int, string) {
	if in.AttemptID == "" || in.TaskID == "" || in.TaskID != in.Source.TaskID ||
		in.TaskID != in.Draft.SourceTaskID || in.Source.Sequence < 1 ||
		in.Source.Sequence != in.Draft.SourceSequence || in.SourceDigest != in.Draft.SourceDigest {
		return 0, "binding_mismatch"
	}
	draftDigest, err := SummaryDraftDigest(in.Draft)
	if err != nil || draftDigest != in.DraftDigest {
		return 0, "draft_digest_mismatch"
	}
	encoded, err := json.Marshal(in.Source.Messages)
	if err != nil || len(encoded) == 0 || len(encoded) > MaxEventPageBytes {
		return 0, "source_invalid"
	}
	digest := sha256.Sum256(encoded)
	if hex.EncodeToString(digest[:]) != in.SourceDigest {
		return 0, "source_digest_mismatch"
	}
	_, checkpoint, err := PrepareContinuation(in.Source, in.Draft.Request)
	if err != nil || checkpoint == nil || in.Draft.Checkpoint == nil ||
		!equalJSON(checkpoint, in.Draft.Checkpoint) {
		return 0, "checkpoint_mismatch"
	}
	return checkpoint.RemovedMessages, ""
}

func equalJSON(a, b any) bool {
	left, leftErr := json.Marshal(a)
	right, rightErr := json.Marshal(b)
	return leftErr == nil && rightErr == nil && bytes.Equal(left, right)
}

func summaryEntries(summary Summary) []string {
	total := len(summary.Requirements) + len(summary.Activity) + len(summary.Decisions) +
		len(summary.PendingWork) + len(summary.Failures) + len(summary.Artifacts)
	entries := make([]string, 0, total)
	for _, category := range [][]string{summary.Requirements, summary.Activity, summary.Decisions, summary.PendingWork, summary.Failures, summary.Artifacts} {
		entries = append(entries, category...)
	}
	return entries
}

func containsUnsafeSummaryDisplay(value string) bool {
	for _, r := range value {
		if r == '\n' || r == '\r' || r == '\t' {
			continue
		}
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			return true
		}
	}
	return false
}

func collectMessageAnchors(out map[string]struct{}, message providers.Message) {
	for _, value := range []string{message.Content, message.ToolCallID} {
		anchors, _ := extractSummaryAnchors(value)
		for _, anchor := range anchors {
			out[anchor] = struct{}{}
		}
	}
	for _, call := range message.ToolCalls {
		for _, value := range []string{call.ID, call.Name, string(call.Arguments)} {
			anchors, _ := extractSummaryAnchors(value)
			for _, anchor := range anchors {
				out[anchor] = struct{}{}
			}
		}
	}
}

func extractSummaryAnchors(value string) ([]string, bool) {
	issues := summaryIssueAnchor.FindAllString(value, -1)
	hexes := summaryHexAnchor.FindAllString(value, -1)
	urls := summaryURLAnchor.FindAllString(value, -1)
	anchors := make([]string, 0, len(issues)+len(hexes)+len(urls))
	for _, anchor := range issues {
		anchors = append(anchors, "issue:"+strings.ToLower(anchor))
	}
	for _, anchor := range hexes {
		anchors = append(anchors, "hex:"+strings.ToLower(anchor))
	}
	oversized := false
	for _, raw := range urls {
		if len(raw) > 512 {
			oversized = true
			continue
		}
		anchors = append(anchors, "url:"+canonicalSummaryURL(raw))
	}
	return anchors, oversized
}

func canonicalSummaryURL(raw string) string {
	split := strings.Index(raw, "://")
	return strings.ToLower(raw[:split]) + raw[split:]
}

func integrityDecision(decision, code string, entries, removed int) SummaryValidationDecision {
	return SummaryValidationDecision{
		Decision: decision,
		Note:     fmt.Sprintf("summary_integrity_v1 code=%s entries=%d removed=%d approval=false", code, entries, removed),
	}
}
