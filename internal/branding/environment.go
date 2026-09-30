// Package branding provides compatibility for the product rename.
package branding

import (
	"os"
	"strings"
)

// LookupEnv prefers NEXUS_ names, including an explicitly empty value. Legacy
// DARWIN_ names remain accepted so existing services retain their settings.
func LookupEnv(key string) (string, bool) {
	canonical := strings.Replace(key, "DARWIN_", "NEXUS_", 1)
	if value, ok := os.LookupEnv(canonical); ok {
		return value, true
	}
	return os.LookupEnv(key)
}

func Getenv(key string) string { value, _ := LookupEnv(key); return value }
