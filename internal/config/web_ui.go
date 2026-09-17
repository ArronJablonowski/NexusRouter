package config

import (
	"errors"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var webUIPath = regexp.MustCompile(`^/[A-Za-z0-9][A-Za-z0-9_-]{0,31}$`)

// Validate keeps the browser surface on a small, unambiguous path and accepts
// only exact HTTP origins. An empty origin list means the server derives its
// sole same-origin value from the request's validated listener authority.
func (w WebUI) Validate(listen string) error {
	if !w.Enabled {
		if w.PathPrefix != "/app" || w.BrowserSessionTTL != "8h" || len(w.AllowedOrigins) != 0 || w.DefaultModel != "" {
			return errors.New("disabled web UI must retain inert defaults")
		}
		return nil
	}
	if w.DefaultModel != "" && !identifier.MatchString(w.DefaultModel) {
		return errors.New("invalid web UI default model")
	}
	if !webUIPath.MatchString(w.PathPrefix) || w.PathPrefix == "/v1" || w.PathPrefix == "/health" {
		return errors.New("invalid web UI path prefix")
	}
	ttl, err := Duration(w.BrowserSessionTTL)
	if err != nil || ttl < 5*time.Minute || ttl > 24*time.Hour {
		return errors.New("invalid browser session TTL")
	}
	if len(w.AllowedOrigins) > 8 {
		return errors.New("too many web UI origins")
	}
	seen := map[string]bool{}
	_, listenPort, listenErr := net.SplitHostPort(listen)
	for _, origin := range w.AllowedOrigins {
		u, err := url.Parse(origin)
		if err != nil || listenErr != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || seen[origin] || !originLoopback(u.Hostname()) || u.Port() != listenPort {
			return errors.New("invalid web UI origin")
		}
		seen[origin] = true
	}
	if _, _, err := net.SplitHostPort(listen); err != nil || strings.ContainsAny(w.PathPrefix, `\\?#%`) {
		return errors.New("invalid web UI listener boundary")
	}
	return nil
}

func originLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
