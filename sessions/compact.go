package sessions

import "darwinrouter/providers"

type Summary struct{ Decisions, PendingWork, Failures, Artifacts []string }
type Compaction struct {
	Version         int
	Summary         Summary
	RemovedMessages int
	Recent          []providers.Message
}

// Compact selects a safe suffix. Summary generation and its validation are
// replaceable external operations; this function never invents summary content.
// A cut inside a parallel tool batch expands backward to its assistant call.
func Compact(messages []providers.Message, keep int, summary Summary) (Compaction, error) {
	out := Compaction{Version: 1, Summary: summary}
	if keep < 1 || len(messages) == 0 {
		return out, ErrHistory
	}
	cut := len(messages) - keep
	if cut < 0 {
		cut = 0
	}
	batchStart := -1
	pending := map[string]bool{}
	used := map[string]bool{}
	for i, m := range messages {
		if len(pending) > 0 && m.Role != "tool" {
			return out, ErrHistory
		}
		if m.Role == "tool" {
			if !pending[m.ToolCallID] {
				return out, ErrHistory
			}
			if cut >= batchStart && cut <= i {
				cut = batchStart
			}
			delete(pending, m.ToolCallID)
		} else if m.Role != "assistant" && m.Role != "user" && m.Role != "system" {
			return out, ErrHistory
		}
		if len(m.ToolCalls) > 0 {
			if m.Role != "assistant" {
				return out, ErrHistory
			}
			batchStart = i
			for _, c := range m.ToolCalls {
				if c.ID == "" || used[c.ID] {
					return out, ErrHistory
				}
				used[c.ID] = true
				pending[c.ID] = true
			}
		}
	}
	if len(pending) > 0 {
		return out, ErrHistory
	}
	out.RemovedMessages = cut
	out.Recent = append([]providers.Message(nil), messages[cut:]...)
	return out, nil
}
