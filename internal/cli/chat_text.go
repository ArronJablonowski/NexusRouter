package cli

import (
	"strings"
	"unicode/utf8"
)

// chatTextFilter belongs to one untrusted text stream. It retains only parser
// state and at most three incomplete UTF-8 bytes, never control-string payloads.
// Do not share it concurrently or feed trusted terminal metadata through it.
type chatTextFilter struct {
	state   int
	osc     bool
	pending string
}

func (f *chatTextFilter) Reset() { *f = chatTextFilter{} }

func (f *chatTextFilter) Write(text string) string { return f.write(text, false) }

// chatSafeText renders a complete untrusted message as plain terminal text.
// It does not interpret Markdown or emit ANSI. Escape strings are consumed as
// units, including unterminated payloads, rather than exposing their contents.
func chatSafeText(text string) string {
	var filter chatTextFilter
	return filter.write(text, true)
}

func (f *chatTextFilter) write(text string, final bool) string {
	const (
		plain = iota
		escape
		csi
		controlString
		stringEscape
		escapeIntermediate
	)
	state, osc := f.state, f.osc
	defer func() { f.state, f.osc = state, osc }()
	if f.pending != "" {
		text = f.pending + text
		f.pending = ""
	}
	var out strings.Builder
	for len(text) > 0 {
		if !final && !utf8.FullRuneInString(text) {
			f.pending = strings.Clone(text)
			break
		}
		r, size := utf8.DecodeRuneInString(text)
		text = text[size:]
		switch state {
		case controlString:
			if r == '\a' && osc || r == 0x9c {
				state = plain
			} else if r == 0x1b {
				state = stringEscape
			}
			continue
		case stringEscape:
			if r == '\\' || r == 0x9c || r == '\a' && osc {
				state = plain
			} else if r != 0x1b {
				state = controlString
			}
			continue
		case csi:
			if r >= 0x40 && r <= 0x7e {
				state = plain
			} else if r == 0x1b {
				state = escape
			}
			continue
		case escapeIntermediate:
			if r >= 0x30 && r <= 0x7e {
				state = plain
			} else if r == 0x1b {
				state = escape
			}
			continue
		case escape:
			switch r {
			case '[':
				state = csi
			case ']', 'P', '_', '^', 'X':
				osc = r == ']'
				state = controlString
			case 0x1b:
				state = escape
			default:
				if r >= 0x20 && r <= 0x2f {
					state = escapeIntermediate
				} else {
					state = plain
				}
			}
			continue
		}
		switch r {
		case 0x1b:
			state = escape
			continue
		case 0x9b:
			state = csi
			continue
		case 0x90, 0x98, 0x9d, 0x9e, 0x9f:
			osc = r == 0x9d
			state = controlString
			continue
		}
		if r < 0x20 && r != '\n' && r != '\t' || r >= 0x7f && r <= 0x9f {
			continue
		}
		// Remove directional marks, embeddings, overrides and isolates. Keep
		// printable RTL letters and emoji joiners intact.
		if r == 0x61c || r == 0x200e || r == 0x200f || r >= 0x202a && r <= 0x202e || r >= 0x2066 && r <= 0x2069 {
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}
