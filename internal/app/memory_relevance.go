package app

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Lexical retrieval is deterministic and local. Unique terms prevent repetition
// from boosting a fact; confidence and immutable ID break equal-overlap ties.
// This is not semantic similarity or authority to follow factual content.
func memoryTerms(text string) map[string]bool {
	stop := " a an the and or of to for in on at is are was were be been it this that with from as by do does how what please redacted "
	out := map[string]bool{}
	for _, term := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if utf8.RuneCountInString(term) >= 2 && !strings.Contains(stop, " "+term+" ") {
			out[term] = true
		}
	}
	return out
}

// Retrieval uses only current user material, never assistant output or tool
// instructions. Historical context remains handled by the session engine.
func memoryTaskQuery(r Request) string {
	if len(r.Messages) == 0 {
		return r.Prompt
	}
	for i := len(r.Messages) - 1; i >= 0; i-- {
		if r.Messages[i].Role == "user" {
			return r.Messages[i].Content
		}
	}
	return ""
}
