package remote

import (
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

// DiscoveryService is a private DNS-SD service type, not a trust mechanism.
const DiscoveryService = "_nexusrouter._tcp.local."

// DiscoveryCandidate is an unauthenticated connection hint. It is deliberately
// not a Peer: it cannot contain permissions, credentials, model quality or work.
// ObservedAt/ExpiresAt are set by the receiving process, never by the sender.
type DiscoveryCandidate struct {
	Version                  int       `json:"version"`
	Instance                 string    `json:"instance"`
	Endpoint                 string    `json:"endpoint"`
	ServerName               string    `json:"server_name"`
	ClaimedCertificateSHA256 string    `json:"claimed_certificate_sha256"`
	SSHPort                  int       `json:"ssh_port,omitempty"`
	Verified                 bool      `json:"verified"`
	ObservedAt               time.Time `json:"observed_at"`
	ExpiresAt                time.Time `json:"expires_at"`
}

// ParseDiscoveryCandidate accepts one complete DNS-SD instance record. It makes
// no network connection or trust mutation. A caller must bind records to the
// selected receiving interface and discard expired/conflicting observations.
// v=1 TXT fields are bounded and closed; unknown fields cannot carry extensions
// that a later pairing UI could accidentally interpret as granted authority.
func ParseDiscoveryCandidate(instance, address string, port int, txt []string, ttl uint32, now time.Time) (DiscoveryCandidate, error) {
	var zero DiscoveryCandidate
	if !id(instance) || port < 1 || port > 65535 || ttl == 0 || now.IsZero() || len(txt) < 4 || len(txt) > 5 {
		return zero, ErrInvalid
	}
	ip, err := netip.ParseAddr(address)
	if err != nil || ip.Zone() != "" || !ip.IsPrivate() || ip.Is4In6() {
		return zero, ErrDenied
	}
	values := map[string]string{}
	size := 0
	for _, entry := range txt {
		size += 1 + len(entry)
		if len(entry) > 255 || size > 1024 {
			return zero, ErrInvalid
		}
		key, value, ok := strings.Cut(entry, "=")
		key = strings.ToLower(key)
		if !ok || value == "" {
			return zero, ErrInvalid
		}
		if _, exists := values[key]; exists {
			return zero, ErrInvalid
		}
		switch key {
		case "v", "id", "name", "pin", "ssh":
		default:
			return zero, ErrInvalid
		}
		values[key] = value
	}
	if values["v"] != "1" || values["id"] != instance || !discoveryDNSName(values["name"]) || !hexDigest(values["pin"]) {
		return zero, ErrInvalid
	}
	ssh := 0
	if value, ok := values["ssh"]; ok {
		ssh, err = strconv.Atoi(value)
		if err != nil || ssh < 1 || ssh > 65535 || strconv.Itoa(ssh) != value {
			return zero, ErrInvalid
		}
	}
	now = now.UTC()
	return DiscoveryCandidate{Version: 1, Instance: instance, Endpoint: "https://" + net.JoinHostPort(ip.String(), strconv.Itoa(port)), ServerName: values["name"], ClaimedCertificateSHA256: values["pin"], SSHPort: ssh, ObservedAt: now, ExpiresAt: now.Add(time.Duration(min(ttl, 120)) * time.Second)}, nil
}
func discoveryDNSName(value string) bool {
	if len(value) == 0 || len(value) > 253 || value != strings.ToLower(value) {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return false
			}
		}
	}
	return true
}
