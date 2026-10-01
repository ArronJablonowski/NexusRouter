// Package textgateway verifies single text completions for native harnesses.
package textgateway

import (
	"errors"
	"github.com/ArronJablonowski/NexusRouter/harness/internal/wirejson"
	"strings"
	"unicode/utf8"
)

var ErrProjection = errors.New("invalid native text completion")

const MaxTextBytes = 4 << 20

func uniqueJSON(body []byte) bool { return wirejson.Unique(body) }
func identifier(s string) bool {
	if s == "" || len(s) > 256 || !utf8.ValidString(s) || strings.TrimSpace(s) != s {
		return false
	}
	for _, r := range s {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}

// Match ECMAScript trimEnd, including BOM but excluding Go's extra NEL space.
