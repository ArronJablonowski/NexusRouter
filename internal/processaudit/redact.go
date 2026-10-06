package processaudit

import (
	"net/url"
	"strings"
)

// Redact preserves argv boundaries while masking conventional secret flags,
// URL credentials/query strings, and opaque inline scripts or prompt content.
// Arbitrary positional secrets cannot be recognized reliably.
func Redact(args []string) []string {
	out := make([]string, len(args))
	hideNext := false
	for i, arg := range args {
		if hideNext {
			out[i] = "[REDACTED]"
			hideNext = false
			continue
		}
		if u, err := url.Parse(arg); err == nil && u.Scheme != "" && u.Host != "" {
			u.User = nil
			u.RawQuery = ""
			u.Fragment = ""
			out[i] = u.String()
			continue
		}
		out[i] = arg
		key, _, assigned := strings.Cut(arg, "=")
		lower := strings.ToLower(key)
		sensitive := false
		for _, word := range []string{"token", "password", "passwd", "secret", "api-key", "api_key", "authorization", "credential"} {
			if strings.Contains(lower, word) {
				sensitive = true
			}
		}
		if lower == "-c" || lower == "-e" || lower == "--eval" || lower == "--command" || lower == "--prompt" || lower == "--message" || lower == "--query" || lower == "--header" || key == "-H" {
			sensitive = true
		}
		if sensitive && i > 0 {
			if assigned {
				out[i] = key + "=[REDACTED]"
			} else {
				out[i] = arg
				hideNext = true
			}
			continue
		}

	}
	return out
}
