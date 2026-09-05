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

func TestChatTextFilterEveryFragmentBoundary(t *testing.T) {
	inputs := []string{
		"数学 👩‍💻 مرحبا שלום\n\t",
		"a\x1b[31mred\x1b[0mz",
		"a\x1b]52;secret\x1b\\z",
		"a\x1b]52;secret\az",
		"a\x1bPsecret\astill hidden\x1b\\z",
		"a\x1b_secret\x1b\\z\x1b^hidden\x1b\\",
		"a\x1bXsecret\x1b\\z\x1b(B!",
		"a\u009dsecret\u009cz\u009b31m!\u009b0m",
		"a\u0090secret\u009cz\u0098hidden\u009c!",
		"a\u061c\u200e\u202e\u2066b\u2069",
		"visible\x1b]payload\x1b[31mhidden",
		"a\xffb\x1b[123;",
	}
	for _, input := range inputs {
		want := chatSafeText(input)
		// Includes splits inside multibyte C1 controls, bidi marks and emoji.
		for split := 0; split <= len(input); split++ {
			var filter chatTextFilter
			got := filter.Write(input[:split]) + filter.Write(input[split:])
			if got != want || !utf8.ValidString(got) {
				t.Fatalf("split %d: got %q want %q", split, got, want)
			}
		}
		var filter chatTextFilter
		var got strings.Builder
		for i := range len(input) {
			fragment := filter.Write(input[i : i+1])
			if !utf8.ValidString(fragment) {
				t.Fatal("emitted incomplete UTF-8")
			}
			got.WriteString(fragment)
		}
		if got.String() != want {
			t.Fatalf("bytewise: got %q want %q", got.String(), want)
		}
	}
}

func TestChatTextFilterUnterminatedPayloadAndReset(t *testing.T) {
	var filter chatTextFilter
	if got := filter.Write("visible\x1b"); got != "visible" {
		t.Fatal("prefix changed")
	}
	for _, chunk := range []string{"]52;", "secret", "\x1b", "[31m", strings.Repeat("hidden", 1<<17)} {
		if got := filter.Write(chunk); got != "" {
			t.Fatal("unterminated control payload leaked")
		}
	}
	if filter.pending != "" {
		t.Fatal("buffered payload")
	}
	filter.Reset()
	if got := filter.Write("fresh task"); got != "fresh task" {
		t.Fatal("reset retained parser state")
	}
	filter.Write("\xe2\x80") // Incomplete UTF-8 must not cross task boundaries.
	if len(filter.pending) != 2 {
		t.Fatal("missing bounded incomplete rune")
	}
	filter.Reset()
	if got := filter.Write("new"); got != "new" || filter.pending != "" {
		t.Fatal("reset retained partial rune")
	}
}
