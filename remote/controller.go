package remote

import (
	"net/netip"
	"net/url"
	"slices"
)

// Controller identifies the authenticated querying commander with dispatch
// authority. It does not disclose other peers or claim current task ownership.
type Controller struct {
	Instance string `json:"instance"`
	Hostname string `json:"hostname,omitempty"`
	IP       string `json:"ip"`
}

func requestingController(peer Peer, hostname string) *Controller {
	if !slices.Contains(peer.Operations, "dispatch") {
		return nil
	}
	endpoint, err := url.Parse(peer.Endpoint)
	if err != nil {
		return nil
	}
	ip, err := netip.ParseAddr(endpoint.Hostname())
	if err != nil {
		return nil
	}
	if !validHostname(hostname) {
		hostname = ""
	}
	return &Controller{Instance: peer.ID, Hostname: hostname, IP: ip.String()}
}
func (c *Controller) valid() bool {
	if c == nil {
		return true
	}
	_, err := netip.ParseAddr(c.IP)
	return c.Instance != "" && len(c.Instance) <= 64 && validHostname(c.Instance) && validHostname(c.Hostname) && err == nil
}
