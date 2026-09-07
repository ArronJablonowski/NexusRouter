package traces

import (
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

type ExportOptions struct {
	Endpoint  string `json:"endpoint"`
	APIKeyEnv string `json:"api_key_env,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

var exportEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

func (o ExportOptions) Validate() error {
	if o.Limit < 0 || o.Limit > MaxTraces || len(o.Endpoint) == 0 || len(o.Endpoint) > 2048 || strings.TrimSpace(o.Endpoint) != o.Endpoint || o.APIKeyEnv != "" && !exportEnvName.MatchString(o.APIKeyEnv) {
		return ErrExport
	}
	u, err := url.Parse(o.Endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || strings.HasSuffix(u.Host, ":") || u.Path == "" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(o.Endpoint, "#") {
		return ErrExport
	}
	if u.Port() != "" {
		port, err := strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 {
			return ErrExport
		}
	}
	if u.Scheme == "http" && !strings.EqualFold(u.Hostname(), "localhost") {
		ip, err := netip.ParseAddr(u.Hostname())
		if err != nil || ip.Zone() != "" || !ip.Unmap().IsLoopback() {
			return ErrExport
		}
	}
	return nil
}

func (o ExportOptions) TraceLimit() int {
	if o.Limit == 0 {
		return 16
	}
	return o.Limit
}
