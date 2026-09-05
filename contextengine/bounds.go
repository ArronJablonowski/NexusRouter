package contextengine

import (
	"unicode/utf8"

	"github.com/ArronJablonowski/DarwinRouter/providers"
)

// CheckMessages bounds raw bytes/counts and checks complete conversations before
// an embedding host decodes or redacts context. It does not call an engine.
// Assemble/SelectCompaction also enforce the stricter serialized-byte limit.
func CheckMessages(bundles ...[]providers.Message) error {
	if !validBundles(bundles...) {
		return ErrEngine
	}
	return nil
}

// Preflight bounds allocation before serialization; serialized escaping is
// checked separately. Each tier is a complete independently valid conversation.
func validBundles(bundles ...[]providers.Message) bool {
	remaining := maxBytes
	reserve := func(n int) bool {
		if n > remaining {
			return false
		}
		remaining -= n
		return true
	}
	text := func(s string) bool { return reserve(len(s)) && utf8.ValidString(s) }
	for _, messages := range bundles {
		if len(messages) > maxBytes/32 {
			return false
		}
		for _, message := range messages {
			if !reserve(32) || !text(message.Role) || !text(message.Content) || !text(message.ToolCallID) || len(message.ToolCalls) > maxBytes/32 {
				return false
			}
			for _, call := range message.ToolCalls {
				if !reserve(32) || !text(call.ID) || !text(call.Name) || !reserve(len(call.Arguments)) || !utf8.Valid(call.Arguments) {
					return false
				}
			}
		}
		if len(messages) > 0 && providers.ValidateMessages(messages) != nil {
			return false
		}
	}
	return true
}
