package tools

import (
	"context"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ExecutionIdentity is host-supplied provenance, not approval. Only a scoped
// executor places it into a handler context after its schema/policy gates.
// It contains no raw arguments, credentials or mutable slices.
type ExecutionIdentity struct {
	TaskID, SessionID, TurnID, AttemptID, ToolCallID, ToolName string
}

type executionIdentityKey struct{}

// ExecutionIdentityFromContext returns a copy of the current admitted call.
// There is intentionally no exported setter. Unscoped execution masks any
// inherited identity so nested host calls cannot impersonate their caller.
func ExecutionIdentityFromContext(ctx context.Context) (ExecutionIdentity, bool) {
	if ctx == nil {
		return ExecutionIdentity{}, false
	}
	identity, ok := ctx.Value(executionIdentityKey{}).(ExecutionIdentity)
	if !ok || !identity.valid() {
		return ExecutionIdentity{}, false
	}
	return identity, true
}

func (identity ExecutionIdentity) valid() bool {
	for _, field := range []struct {
		value string
		max   int
	}{{identity.TaskID, 128}, {identity.SessionID, 128}, {identity.TurnID, 128}, {identity.AttemptID, 128}, {identity.ToolCallID, 256}, {identity.ToolName, 64}} {
		if len(field.value) > field.max || strings.TrimSpace(field.value) == "" || !utf8.ValidString(field.value) {
			return false
		}
		for _, r := range field.value {
			if unicode.IsControl(r) {
				return false
			}
		}
	}
	return true
}
