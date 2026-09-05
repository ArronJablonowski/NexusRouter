package app

import (
	"sort"
	"strings"
)

// textRedactor incrementally applies the same ordered literal replacements as
// redact. A stage withholds only a possible secret prefix, so chunk boundaries
// cannot expose a partial secret. Call Flush only when the text is complete.
// It operates on bytes; its output chunks need not end at UTF-8 boundaries.
// Like a strings.Builder, it is not safe for concurrent use.
type textRedactor struct {
	stages []literalRedactor
}

func newTextRedactor(secrets []string) *textRedactor {
	ordered := append([]string(nil), secrets...)
	sort.Slice(ordered, func(i, j int) bool { return len(ordered[i]) > len(ordered[j]) })
	r := &textRedactor{}
	for _, secret := range ordered {
		if secret == "" {
			continue
		}
		stage := literalRedactor{secret: secret, failure: make([]int, len(secret))}
		for i, matched := 1, 0; i < len(secret); i++ {
			for matched > 0 && secret[i] != secret[matched] {
				matched = stage.failure[matched-1]
			}
			if secret[i] == secret[matched] {
				matched++
			}
			stage.failure[i] = matched
		}
		r.stages = append(r.stages, stage)
	}
	return r
}

func (r *textRedactor) Write(text string) string {
	for i := range r.stages {
		text = r.stages[i].write(text)
	}
	return text
}

// Flush releases unmatched suffixes through every remaining replacement stage.
// Afterwards the redactor can be reused for a separate text, with no matches
// spanning the boundary. Repeated calls without intervening writes return "".
func (r *textRedactor) Flush() string {
	var output strings.Builder
	for i := range r.stages {
		stage := &r.stages[i]
		text := stage.secret[:stage.matched]
		stage.matched = 0
		for j := i + 1; j < len(r.stages); j++ {
			text = r.stages[j].write(text)
		}
		output.WriteString(text)
	}
	return output.String()
}

// literalRedactor is a streaming KMP matcher. Pending bytes are represented by
// a prefix length into secret, never by a slice retaining an input chunk.
// Failure links ensure linear scanning even for long overlapping prefixes.
type literalRedactor struct {
	secret  string
	failure []int
	matched int
}

func (r *literalRedactor) write(text string) string {
	if text == "" {
		return ""
	}
	var output strings.Builder
	output.Grow(len(text))
	for i := 0; i < len(text); i++ {
		for r.matched > 0 && text[i] != r.secret[r.matched] {
			next := r.failure[r.matched-1]
			output.WriteString(r.secret[:r.matched-next])
			r.matched = next
		}
		if text[i] == r.secret[r.matched] {
			r.matched++
			if r.matched == len(r.secret) {
				output.WriteString("[REDACTED]")
				r.matched = 0
			}
		} else {
			output.WriteByte(text[i])
		}
	}
	return output.String()
}
