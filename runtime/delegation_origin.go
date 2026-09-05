package runtime

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// DelegationOrigin binds newly created work to its parent's admitted tool
// invocation. It contains no prompt or arguments and grants no permissions.
// Historical work may omit it; an absent origin is not an inferred binding.
type DelegationOrigin struct {
	Version    int    `json:"version"`
	TurnID     string `json:"turn_id"`
	AttemptID  string `json:"attempt_id"`
	ToolCallID string `json:"tool_call_id"`
	ToolName   string `json:"tool_name"`
	BatchIndex *int   `json:"batch_index,omitempty"`
}

func (o DelegationOrigin) Validate() error {
	bad := errors.New("invalid delegation origin")
	valid := func(s string, max int) bool {
		if len(s) > max || strings.TrimSpace(s) == "" || !utf8.ValidString(s) {
			return false
		}
		for _, r := range s {
			if unicode.IsControl(r) {
				return false
			}
		}
		return true
	}
	if o.Version != 1 || !valid(o.TurnID, 128) || !valid(o.AttemptID, 128) || !valid(o.ToolCallID, 256) {
		return bad
	}
	switch o.ToolName {
	case "delegate":
		if o.BatchIndex != nil {
			return bad
		}
	case "delegate_batch":
		if o.BatchIndex == nil || *o.BatchIndex < 0 || *o.BatchIndex > 3 {
			return bad
		}
	default:
		return bad
	}
	return nil
}

// Clone owns the optional index rather than borrowing mutable caller storage.
func (o DelegationOrigin) Clone() *DelegationOrigin {
	if o.BatchIndex != nil {
		index := *o.BatchIndex
		o.BatchIndex = &index
	}
	return &o
}
