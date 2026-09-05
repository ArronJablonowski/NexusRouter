package sessions

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

type Summary = runtime.ContextSummary
type Compaction struct {
	Version         int
	Summary         Summary
	RemovedMessages int
	Recent          []providers.Message
}

// Compact selects a safe suffix. Summary generation and its validation are
// replaceable external operations; this function never invents summary content.
// A cut inside a parallel tool batch expands backward to its assistant call.
// The returned record owns its slices, including tool arguments and summary
// entries, so subsequent caller mutations cannot change a selected checkpoint.
// Structural validation does not establish that a summary is accurate.
func Compact(messages []providers.Message, keep int, summary Summary) (Compaction, error) {
	if keep < 1 || providers.ValidateMessages(messages) != nil || !validSummary(summary) {
		return Compaction{}, ErrHistory
	}
	out := Compaction{Version: 1, Summary: cloneSummary(summary)}
	cut := len(messages) - keep
	if cut < 0 {
		cut = 0
	}
	batchStart := -1
	for i, m := range messages {
		if !utf8.ValidString(m.Content) || !utf8.ValidString(m.ToolCallID) {
			return Compaction{}, ErrHistory
		}
		if m.Role == "tool" {
			if cut >= batchStart && cut <= i {
				cut = batchStart
			}
		}
		if len(m.ToolCalls) > 0 {
			batchStart = i
			for _, c := range m.ToolCalls {
				if !utf8.ValidString(c.ID) || !utf8.ValidString(c.Name) || !utf8.Valid(c.Arguments) {
					return Compaction{}, ErrHistory
				}
			}
		}
	}
	out.RemovedMessages = cut
	out.Recent = append([]providers.Message(nil), messages[cut:]...)
	for i := range out.Recent {
		out.Recent[i].ToolCalls = append([]providers.ToolCall(nil), out.Recent[i].ToolCalls...)
		for j := range out.Recent[i].ToolCalls {
			call := &out.Recent[i].ToolCalls[j]
			call.Arguments = append(json.RawMessage(nil), call.Arguments...)
		}
	}
	return out, nil
}

func validSummary(summary Summary) bool {
	bytes := 0
	for _, items := range [][]string{summary.Requirements, summary.Activity, summary.Decisions, summary.PendingWork, summary.Failures, summary.Artifacts} {
		if len(items) > 128 {
			return false
		}
		for _, item := range items {
			if len(item) > 64<<10 || !utf8.ValidString(item) || strings.TrimSpace(item) == "" {
				return false
			}
			bytes += len(item)
			if bytes > 64<<10 {
				return false
			}
		}
	}
	encoded, err := json.Marshal(summary)
	return err == nil && len(encoded) <= 64<<10
}

func cloneSummary(summary Summary) Summary {
	return Summary{
		Requirements: append([]string(nil), summary.Requirements...),
		Activity:     append([]string(nil), summary.Activity...),
		Decisions:    append([]string(nil), summary.Decisions...),
		PendingWork:  append([]string(nil), summary.PendingWork...),
		Failures:     append([]string(nil), summary.Failures...),
		Artifacts:    append([]string(nil), summary.Artifacts...),
	}
}
