package goose

import (
	"net/url"
	"os"
	"path/filepath"
	"strconv"
)

// isolatedEnvironment contains only private state and the disposable child token.
// The caller owns directory cleanup and must not pass real provider credentials.
func isolatedEnvironment(dir, base, key, model string) ([]string, error) {
	u, e := url.Parse(base)
	if e != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Path != "/v1" || u.RawPath != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil || !label(key) || !label(model) || !filepath.IsAbs(dir) {
		return nil, ErrProjection
	}
	port, e := strconv.Atoi(u.Port())
	if e != nil || port < 1 || port > 65535 {
		return nil, ErrProjection
	}
	info, e := os.Lstat(dir)
	if e != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, ErrProjection
	}
	entries, e := os.ReadDir(dir)
	if e != nil || len(entries) != 0 {
		return nil, ErrProjection
	}
	return []string{"PATH=" + os.Getenv("PATH"), "GOOSE_PATH_ROOT=" + dir, "OPENAI_BASE_URL=" + base, "OPENAI_API_KEY=" + key, "GOOSE_DISABLE_SESSION_NAMING=true", "NO_COLOR=1"}, nil
}
