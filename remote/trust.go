package remote

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"net"
	"net/netip"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Peer is installed by an administrator out of band. Pins are SHA-256 leaf
// certificate fingerprints, verified in addition to normal CA/expiry checks.
// Two pins permit explicit overlap during rotation. Removing a peer or pin
// takes effect on the next request, including an existing server connection.
type Peer struct {
	RequestLimits       *RequestLimits `json:"request_limits,omitempty"`
	Harnesses           []string       `json:"harnesses,omitempty"`
	Transport           string         `json:"transport,omitempty"`
	SSH                 *SSH           `json:"ssh,omitempty"`
	ID                  string         `json:"id"`
	Endpoint            string         `json:"endpoint"`
	ServerName          string         `json:"server_name"`
	Pins                []string       `json:"pins"`
	Operations          []string       `json:"operations"`
	Models              []string       `json:"models"`
	AllowPrivate        bool           `json:"allow_private"`
	AllowPublicNetwork  bool           `json:"allow_public_network"`
	AllowCloudInference bool           `json:"allow_cloud_inference"`
	MaxCost             float64        `json:"max_cost"`
	MaxContextTokens    int            `json:"max_context_tokens"`
}
type Registry struct {
	Version int    `json:"version"`
	Peers   []Peer `json:"peers"`
}

// TrustFile is an owner-private JSON registry. Replace atomically to rotate or
// revoke; invalid, missing or permissive files fail closed, never use a cache.
type TrustFile string

func (f TrustFile) Read() (Registry, error) {
	var out Registry
	st, err := os.Lstat(string(f))
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || st.Size() > MaxBody {
		return out, ErrDenied
	}
	file, err := os.Open(string(f))
	if err != nil {
		return out, ErrDenied
	}
	defer file.Close()
	actual, err := file.Stat()
	if err != nil || !os.SameFile(st, actual) {
		return out, ErrDenied
	}
	d := json.NewDecoder(io.LimitReader(file, MaxBody+1))
	d.DisallowUnknownFields()
	if d.Decode(&out) != nil || out.Version != Version || len(out.Peers) > 128 {
		return Registry{}, ErrDenied
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return Registry{}, ErrDenied
	}
	seen := map[string]bool{}
	pins := map[string]bool{}
	for _, p := range out.Peers {
		if p.Validate() != nil || seen[p.ID] {
			return Registry{}, ErrDenied
		}
		seen[p.ID] = true
		for _, pin := range p.Pins {
			if pins[pin] {
				return Registry{}, ErrDenied
			}
			pins[pin] = true
		}
	}
	return out, nil
}
func (p Peer) Validate() error {
	if p.RequestLimits != nil && !p.RequestLimits.valid() {
		return ErrInvalid
	}
	if len(p.Harnesses) > 256 {
		return ErrInvalid
	}
	seenHarnesses := map[string]bool{}
	for _, h := range p.Harnesses {
		if !name(h) || h == "auto" || seenHarnesses[h] {
			return ErrInvalid
		}
		seenHarnesses[h] = true
	}
	switch p.Transport {
	case "", "https":
		if p.SSH != nil {
			return ErrInvalid
		}
	case "ssh":
		if p.SSH == nil || p.SSH.Validate() != nil {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	if !id(p.ID) || len(p.Pins) < 1 || len(p.Pins) > 2 || len(p.Operations) < 1 || len(p.Operations) > 5 || len(p.Models) > 128 || !name(p.ServerName) || p.MaxContextTokens < 1 || p.MaxCost < 0 || math.IsNaN(p.MaxCost) || math.IsInf(p.MaxCost, 0) {
		return ErrInvalid
	}
	if _, err := p.address(); err != nil {
		return err
	}
	for _, v := range p.Pins {
		b, e := hex.DecodeString(v)
		if e != nil || len(b) != 32 || strings.ToLower(v) != v {
			return ErrInvalid
		}
	}
	ops := map[string]bool{}
	for _, op := range p.Operations {
		if !slices.Contains([]string{"info", "dispatch", "inspect", "cancel", "logs"}, op) || ops[op] {
			return ErrInvalid
		}
		ops[op] = true
	}
	for _, m := range p.Models {
		if !name(m) || m == "auto" {
			return ErrInvalid
		}
	}
	return nil
}
func (p Peer) address() (string, error) {
	u, e := url.Parse(p.Endpoint)
	if e != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.RawPath != "" || u.Fragment != "" || u.Path != "" || u.Opaque != "" {
		return "", ErrInvalid
	}
	host, port, e := net.SplitHostPort(u.Host)
	n, parseErr := strconv.Atoi(port)
	if e != nil || parseErr != nil || n < 1 || n > 65535 {
		return "", ErrInvalid
	}
	ip, e := netip.ParseAddr(host)
	if e != nil || ip.Zone() != "" || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() {
		return "", ErrDenied
	}
	ip = ip.Unmap()
	if !p.AllowPublicNetwork && !ip.IsPrivate() && !ip.IsLoopback() {
		return "", ErrDenied
	}
	return u.Host, nil
}
func Fingerprint(c *x509.Certificate) string {
	d := sha256.Sum256(c.Raw)
	return hex.EncodeToString(d[:])
}
func (r Registry) peer(id string) (Peer, error) {
	for _, p := range r.Peers {
		if p.ID == id {
			return p, nil
		}
	}
	return Peer{}, ErrDenied
}
func (r Registry) authenticate(c *x509.Certificate) (Peer, error) {
	if c == nil || time.Now().Before(c.NotBefore) || !time.Now().Before(c.NotAfter) {
		return Peer{}, ErrDenied
	}
	pin := Fingerprint(c)
	for _, p := range r.Peers {
		if slices.Contains(p.Pins, pin) {
			return p, nil
		}
	}
	return Peer{}, ErrDenied
}
func (p Peer) permits(op string) bool { return slices.Contains(p.Operations, op) }
func (p Peer) permitsTask(t Task) bool {
	if (t.HarnessID != "" && !slices.Contains(p.Harnesses, t.HarnessID)) || t.Validate() != nil || !p.permits("dispatch") || !slices.Contains(p.Models, t.ModelID) || t.MaxCost > p.MaxCost || t.ContextTokens > p.MaxContextTokens {
		return false
	}
	if !t.Private && !p.AllowCloudInference {
		return false
	}
	if t.Private {
		u, _ := url.Parse(p.Endpoint)
		ip, e := netip.ParseAddr(u.Hostname())
		if !p.AllowPrivate || e != nil || !ip.Unmap().IsPrivate() && !ip.IsLoopback() {
			return false
		}
	}
	return true
}
