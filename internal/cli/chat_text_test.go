package cli

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestChatSafeTextTerminalControls(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"SGR", "before\x1b[31mred\x1b[0mafter", "beforeredafter"},
		{"cursor clear", "a\x1b[2J\x1b[H\x1b[?25lb", "ab"},
		{"OSC clipboard BEL", "a\x1b]52;c;c2VjcmV0\ab", "ab"},
		{"OSC clipboard ST", "a\x1b]52;c;c2VjcmV0\x1b\\b", "ab"},
		{"hyperlinks", "\x1b]8;;https://evil.test\x1b\\label\x1b]8;;\x1b\\", "label"},
		{"DCS", "a\x1bPpayload\x1b\\b", "ab"},
		{"DCS BEL not terminator", "a\x1bPpayload\astill payload\x1b\\b", "ab"},
		{"APC", "a\x1b_payload\x1b\\b", "ab"},
		{"PM", "a\x1b^payload\x1b\\b", "ab"},
		{"SOS", "a\x1bXpayload\x1b\\b", "ab"},
		{"C1 sequences", "a\u009d52;payload\u009c\u009b31mb\u009b0m", "ab"},
		{"C1 DCS", "a\u0090payload\u009cb", "ab"},
		{"single ESC", "safe\x1b", "safe"},
		{"unterminated CSI", "safe\x1b[123;", "safe"},
		{"unterminated OSC", "safe\x1b]52;secret", "safe"},
		{"embedded ESC payload", "a\x1b]payload\x1b[31mhidden\x1b\\b", "ab"},
		{"charset", "a\x1b(Bb", "ab"},
		{"C0", "a\x00\a\b\r\v\f\x1f\x7fb\n\tc", "ab\n\tc"},
		{"C1 other", "a\u0085\u0080\u009cb", "ab"},
		{"bidi", "a\u061c\u200e\u200f\u202a\u202b\u202c\u202d\u202e\u2066\u2067\u2068\u2069b", "ab"},
		{"Unicode", "数学 ∑ x² ≤ ∞ — café 👩‍💻 مرحبا שלום\n\t**plain**", "数学 ∑ x² ≤ ∞ — café 👩‍💻 مرحبا שלום\n\t**plain**"},
		{"malformed UTF8", "a\xffb", "a�b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := chatSafeText(tc.input)
			if got != tc.want || !utf8.ValidString(got) {
				t.Fatalf("got %q want %q", got, tc.want)
			}
			if chatSafeText(got) != got {
				t.Fatal("not idempotent")
			}
		})
	}
}

func TestChatSafeTextLargeUnterminatedPayload(t *testing.T) {
	text := "visible\x1b]52;" + strings.Repeat("x\x1b[", 1<<18)
	if got := chatSafeText(text); got != "visible" {
		t.Fatal("payload escaped", len(got))
	}
}
