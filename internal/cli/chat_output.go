package cli

import "strings"

// chatQuotedText prefixes each explicit model-output line, including lines
// split across callbacks. Apply only after terminal-control filtering, which
// removes carriage returns and cursor controls that could erase the prefix.
// This is presentation separation, not cryptographic output authentication.
func chatQuotedText(text string, lineStart *bool) string {
	var out strings.Builder
	for _, r := range text {
		if *lineStart {
			out.WriteString("| ")
			*lineStart = false
		}
		out.WriteRune(r)
		if r == '\n' {
			*lineStart = true
		}
	}
	return out.String()
}
