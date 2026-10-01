package webuiapp

import (
	"context"
	"github.com/ArronJablonowski/NexusRouter/remote"
	"net/http"
	"time"
)

// RemoteDiscoverer is fixed by the host. Browsers cannot select an interface,
// destination, timeout or credentials, and discovery cannot modify membership.
type RemoteDiscoverer interface {
	Discover(context.Context) ([]remote.DiscoveryCandidate, error)
}

type InterfaceRemoteDiscoverer struct{ Interface string }

func (d InterfaceRemoteDiscoverer) Discover(ctx context.Context) ([]remote.DiscoveryCandidate, error) {
	return remote.DiscoverUnpaired(ctx, d.Interface, 3*time.Second)
}

type remoteDiscoveryPage struct {
	Version    int                         `json:"version"`
	Candidates []remote.DiscoveryCandidate `json:"candidates"`
}

func (h *Handler) serveRemoteDiscovery(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != h.basePath+"/api/v1/remote-discovery" {
		return false
	}
	if r.Method != http.MethodPost {
		h.authenticatedAPINotFound(w, r)
		return true
	}
	if !h.requireMutationAuthority(w, r) {
		return true
	}
	var input struct {
		Version int `json:"version"`
	}
	if decodeMutationJSON(r, &input, 1024) != nil || input.Version != 1 {
		h.writeError(w, r, http.StatusBadRequest, "invalid_request")
		return true
	}
	if h.remoteDiscoverer == nil || h.remoteTrustFile == "" {
		h.writeError(w, r, http.StatusServiceUnavailable, "discovery_unavailable")
		return true
	}
	// Membership must already be administratively enabled, but the scan does not
	// contact peers or consume their configured client credentials.
	if _, err := remote.TrustFile(h.remoteTrustFile).Read(); err != nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "discovery_unavailable")
		return true
	}
	if !h.discoveryActive.CompareAndSwap(false, true) {
		h.writeError(w, r, http.StatusServiceUnavailable, "discovery_busy")
		return true
	}
	defer h.discoveryActive.Store(false)
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	candidates, err := safeCall(func() ([]remote.DiscoveryCandidate, error) { return h.remoteDiscoverer.Discover(ctx) })
	if err != nil || ctx.Err() != nil || len(candidates) > 64 {
		h.writeError(w, r, http.StatusServiceUnavailable, "discovery_unavailable")
		return true
	}
	now := time.Now()
	for _, c := range candidates {
		if c.Verified || c.Version != 1 || c.ObservedAt.After(now) || !c.ExpiresAt.After(now) || c.ExpiresAt.Sub(c.ObservedAt) > 120*time.Second {
			h.writeError(w, r, http.StatusServiceUnavailable, "discovery_unavailable")
			return true
		}
	}
	if candidates == nil {
		candidates = []remote.DiscoveryCandidate{}
	}
	h.writeJSON(w, http.StatusOK, remoteDiscoveryPage{Version: 1, Candidates: candidates})
	return true
}
