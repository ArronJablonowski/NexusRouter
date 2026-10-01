package config

import (
	"errors"
	"net"
	"net/url"
	"path/filepath"
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
		if w.RemoteAutomaticEvidenceDirectory != "" || w.RemoteDispatchDirectory != "" || w.RemoteTaskControls || w.RemoteClient != nil || w.RemoteTrustFile != "" || w.PathPrefix != "/app" || w.BrowserSessionTTL != "8h" || w.ModelInventoryRefreshInterval != "10s" || len(w.AllowedOrigins) != 0 || w.DefaultModel != "" || w.CommanderFallbackModel != "" || w.SpecialistsAllowCloud {
			return errors.New("disabled web UI must retain inert defaults")
		}
		return nil
	}
	if w.RemoteTrustFile != "" && (!filepath.IsAbs(w.RemoteTrustFile) || filepath.Clean(w.RemoteTrustFile) != w.RemoteTrustFile || len(w.RemoteTrustFile) > 4096 || strings.ContainsAny(w.RemoteTrustFile, "\x00\r\n")) {
		return errors.New("invalid remote trust file")
	}
	if dir := w.RemoteDispatchDirectory; dir != "" && (!w.RemoteTaskControls || !filepath.IsAbs(dir) || filepath.Clean(dir) != dir || len(dir) > 4096 || strings.ContainsAny(dir, "\x00\r\n")) {
		return errors.New("remote dispatch requires task controls and a private absolute evidence directory")
	}
	if dir := w.RemoteAutomaticEvidenceDirectory; dir != "" && (w.RemoteDispatchDirectory == "" || !filepath.IsAbs(dir) || filepath.Clean(dir) != dir || len(dir) > 4096 || strings.ContainsAny(dir, "\x00\r\n")) {
		return errors.New("automatic remote routing requires dispatch and a private absolute evidence directory")
	}
	if w.RemoteTaskControls && w.RemoteClient == nil {
		return errors.New("remote task controls require remote client credentials")
	}
	if w.RemoteClient != nil {
		if w.RemoteTrustFile == "" {
			return errors.New("remote browser inspection requires a trust registry")
		}
		for _, path := range []string{w.RemoteClient.CertificateFile, w.RemoteClient.KeyFile, w.RemoteClient.CAFile} {
			if !filepath.IsAbs(path) || filepath.Clean(path) != path || len(path) > 4096 || strings.ContainsAny(path, "\x00\r\n") {
				return errors.New("invalid remote browser credential path")
			}
		}
	}
	if w.DefaultModel != "" && !identifier.MatchString(w.DefaultModel) {
		return errors.New("invalid web UI default model")
	}
	if w.CommanderFallbackModel != "" && !identifier.MatchString(w.CommanderFallbackModel) {
		return errors.New("invalid web UI commander fallback model")
	}
	if !webUIPath.MatchString(w.PathPrefix) || w.PathPrefix == "/v1" || w.PathPrefix == "/health" {
		return errors.New("invalid web UI path prefix")
	}
	ttl, err := Duration(w.BrowserSessionTTL)
	if err != nil || ttl < 5*time.Minute || ttl > 24*time.Hour {
		return errors.New("invalid browser session TTL")
	}
	refresh, err := Duration(w.ModelInventoryRefreshInterval)
	if err != nil || refresh < 5*time.Second || refresh > 5*time.Minute || refresh%time.Millisecond != 0 {
		return errors.New("invalid model inventory refresh interval")
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
