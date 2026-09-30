package codexbridge

import (
	"unicode/utf8"

	"github.com/ArronJablonowski/NexusRouter/providers"
)

// Bound input before JSON serialization, including empty-element overhead.
// Invalid UTF-8 must not become equal to a different, replacement-character
// string through encoding/json's lossy normalization.
func validRequest(r providers.Request) bool {
	// Codex app-server does not currently expose a verified hard output-token
	// ceiling. Reject every bounded request at this boundary instead of silently
	// accepting a budget that the bridge cannot enforce.
	if r.MaxOutputTokens != 0 {
		return false
	}
	remaining := maxExchangeBytes
	reserve := func(n int) bool {
		if n > remaining {
			return false
		}
		remaining -= n
		return true
	}
	text := func(s string) bool { return reserve(len(s)) && utf8.ValidString(s) }
	raw := func(b []byte) bool { return reserve(len(b)) && utf8.Valid(b) }
	if len(r.Messages) > maxExchangeBytes/32 || len(r.Tools) > maxExchangeBytes/32 || !text(r.Model) || !raw(r.JSONSchema) {
		return false
	}
	for _, m := range r.Messages {
		if len(m.ToolCalls) > maxExchangeBytes/32 || !reserve(32) || !text(m.Role) || !text(m.Content) || !text(m.ToolCallID) {
			return false
		}
		for _, c := range m.ToolCalls {
			if !reserve(32) || !text(c.ID) || !text(c.Name) || !raw(c.Arguments) {
				return false
			}
		}
	}
	for _, t := range r.Tools {
		if !reserve(32) || !text(t.Name) || !text(t.Description) || !raw(t.Parameters) {
			return false
		}
	}
	return true
}
