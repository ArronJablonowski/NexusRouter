package config

import (
	"github.com/ArronJablonowski/NexusRouter/policy"
	"path/filepath"
)

// Full capture includes managed logging; privileged subprocess capture is a
// separate installation requirement and is never implied by this setting.
func (s Settings) DNSAudit() policy.DNSAudit {
	if s.Telemetry.DNSLogging == "" {
		return policy.DNSAudit{}
	}
	return policy.DNSAudit{Path: filepath.Dir(s.Telemetry.Database), Source: "nexusrouter-managed-resolver"}
}
