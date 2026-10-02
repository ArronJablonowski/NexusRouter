package config

import (
	"encoding/hex"
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
	for _, m := range s.Models {
		if m.WarmRAMBytes == 0 && m.ResidencyDigest == "" {
			continue
		}
		found := false
		for _, p := range s.Providers {
			if p.ID == m.Provider {
				found = p.DedicatedWarmMemory
			}
		}
		d, err := hex.DecodeString(m.ResidencyDigest)
		if !found || err != nil || len(d) != 32 || strings.ToLower(m.ResidencyDigest) != m.ResidencyDigest || m.WarmRAMBytes < 1<<30 || m.WarmRAMBytes >= m.RAMBytes || m.WarmRAMBytes < m.RAMBytes/2 || m.Locality != "local" || m.VRAMBytes != 0 || m.GPUDevice != "" || m.ContextTokens < 1 {
			return errors.New("invalid dedicated warm memory estimate or model digest")
		}
	}
	for i, p := range s.Providers {
		if !p.ManageResidency && !p.DedicatedWarmMemory {
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
		if p.DedicatedWarmMemory && (p.ManageResidency || len(seen) != 1 || s.Hardware.Concurrent != "1" || s.Workers.Max != 1) {
			return errors.New("warm memory requires one dedicated model, serial execution and no managed unloading")
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
