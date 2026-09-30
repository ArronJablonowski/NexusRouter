package config

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/ArronJablonowski/NexusRouter/providers"
)

// Managed residency is explicit authority to unload configured idle models on
// a dedicated local server. It is not an ownership lock against other clients.
func (s Settings) validateResidency() error {
	for i, p := range s.Providers {
		if !p.ManageResidency {
			continue
		}
		endpoint := p.ResolvedEndpoint()
		u, err := url.Parse(endpoint)
		port, local := residencyEndpoint(endpoint)
		if err != nil || !local || p.Kind != "ollama" || u.User != nil || u.ForceQuery || u.RawQuery != "" || u.Fragment != "" || u.RawFragment != "" || u.RawPath != "" || (u.Path != "" && u.Path != "/") {
			return errors.New("managed residency requires a dedicated loopback Ollama root endpoint")
		}
		for j, other := range s.Providers {
			otherPort, otherLocal := residencyEndpoint(other.ResolvedEndpoint())
			if i != j && otherLocal && port == otherPort {
				return errors.New("managed residency endpoint cannot be shared by provider entries")
			}
		}
		seen := map[string]bool{}
		for _, m := range s.Models {
			if m.Provider != p.ID {
				continue
			}
			identity, err := providers.OllamaModelIdentity(m.Model)
			if err != nil || m.Locality != "local" || seen[identity] {
				return errors.New("managed residency requires distinct local model identities")
			}
			seen[identity] = true
		}
	}
	return nil
}

// Treat loopback aliases conservatively as one server per port, regardless of
// path or scheme. Do not resolve DNS or assert that different ports are owned.
func residencyEndpoint(endpoint string) (string, bool) {
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return "", false
	}
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "", false
	}
	return strconv.Itoa(n), true
}
