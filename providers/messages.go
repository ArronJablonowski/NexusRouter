package providers

import "errors"

// ValidateMessages requires complete tool batches at provider handoff. A model
// must never receive an orphan result, duplicate call identity, or an unanswered
// tool proposal followed by another conversational message.
func ValidateMessages(messages []Message) error {
	invalid := errors.New("invalid model conversation")
	if len(messages) == 0 {
		return invalid
	}
	pending := map[string]bool{}
	used := map[string]bool{}
	for _, m := range messages {
		if m.Role != "system" && m.Role != "user" && m.Role != "assistant" && m.Role != "tool" {
			return invalid
		}
		if len(pending) > 0 && m.Role != "tool" {
			return invalid
		}
		if m.Role == "tool" {
			if !pending[m.ToolCallID] || len(m.ToolCalls) > 0 {
				return invalid
			}
			delete(pending, m.ToolCallID)
		} else if m.ToolCallID != "" {
			return invalid
		}
		if len(m.ToolCalls) > 0 {
			if m.Role != "assistant" || len(m.ToolCalls) > 128 {
				return invalid
			}
			for _, call := range m.ToolCalls {
				if call.ID == "" || call.Name == "" || used[call.ID] || !jsonObject(call.Arguments) {
					return invalid
				}
				used[call.ID] = true
				pending[call.ID] = true
			}
		}
	}
	if len(pending) > 0 {
		return invalid
	}
	return nil
}
