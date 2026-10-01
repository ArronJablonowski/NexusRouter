package webuiapp

import (
	"errors"
	"net/http"
	"path/filepath"

	"github.com/ArronJablonowski/NexusRouter/remote"
)

// The operator fixes this path at startup. Browser inputs never choose a file,
// invoke a network connection or grant trust based on discovery alone.
type membershipRequest struct {
	Version          int          `json:"version"`
	Action           string       `json:"action"`
	ExpectedDigest   string       `json:"expected_digest"`
	IdentityVerified bool         `json:"identity_verified"`
	Peer             *remote.Peer `json:"peer,omitempty"`
	Instance         string       `json:"instance,omitempty"`
}

type membershipPage struct {
	AutomaticEnabled    bool             `json:"automatic_enabled"`
	DispatchEnabled     bool             `json:"dispatch_enabled"`
	TaskControlsEnabled bool             `json:"task_controls_enabled"`
	InspectionEnabled   bool             `json:"inspection_enabled"`
	Version             int              `json:"version"`
	Enabled             bool             `json:"enabled"`
	Digest              string           `json:"digest,omitempty"`
	Registry            *remote.Registry `json:"registry,omitempty"`
}

func (h *Handler) serveRemoteMembership(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != h.basePath+"/api/v1/remote-membership" {
		return false
	}
	if !h.authenticated(r) {
		h.writeError(w, r, http.StatusUnauthorized, "unauthorized")
		return true
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		h.writeError(w, r, http.StatusNotFound, "not_found")
		return true
	}
	if r.Method == http.MethodGet && !strictBrowserGET(r) {
		h.writeError(w, r, http.StatusBadRequest, "invalid_request")
		return true
	}
	if r.Method == http.MethodPost && !h.requireMutationAuthority(w, r) {
		return true
	}
	if !mutationSlot(h, false) {
		h.writeError(w, r, http.StatusServiceUnavailable, "mutation_capacity")
		return true
	}
	defer releaseMutationSlot(h, false)
	if h.remoteTrustFile == "" {
		if r.Method == http.MethodGet {
			h.writeJSON(w, http.StatusOK, membershipPage{Version: 1})
		} else {
			h.writeError(w, r, http.StatusServiceUnavailable, "membership_unavailable")
		}
		return true
	}
	if !filepath.IsAbs(h.remoteTrustFile) || filepath.Clean(h.remoteTrustFile) != h.remoteTrustFile {
		h.writeError(w, r, http.StatusServiceUnavailable, "membership_unavailable")
		return true
	}
	trust := remote.TrustFile(h.remoteTrustFile)
	registry, err := trust.Read()
	if err != nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "membership_unavailable")
		return true
	}
	if r.Method == http.MethodPost {
		var input membershipRequest
		if decodeMutationJSON(r, &input, remote.MaxBody) != nil || input.Version != 1 || input.ExpectedDigest == "" || input.ExpectedDigest == "absent" {
			h.writeError(w, r, http.StatusBadRequest, "invalid_request")
			return true
		}
		switch input.Action {
		case "pair":
			if input.Peer == nil || input.Instance != "" || !input.IdentityVerified || input.Peer.Validate() != nil {
				h.writeError(w, r, http.StatusBadRequest, "invalid_request")
				return true
			}
			registry, err = trust.Pair(*input.Peer, input.ExpectedDigest)
		case "revoke":
			if input.Peer != nil || input.Instance == "" || input.IdentityVerified {
				h.writeError(w, r, http.StatusBadRequest, "invalid_request")
				return true
			}
			registry, err = trust.Revoke(input.Instance, input.ExpectedDigest)
		default:
			h.writeError(w, r, http.StatusBadRequest, "invalid_request")
			return true
		}
		if errors.Is(err, remote.ErrConflict) {
			h.writeError(w, r, http.StatusConflict, "membership_changed")
			return true
		}
		if err != nil {
			h.writeError(w, r, http.StatusServiceUnavailable, "membership_unavailable")
			return true
		}
	}
	h.writeJSON(w, http.StatusOK, membershipPage{AutomaticEnabled: h.remoteAutomatic != nil, DispatchEnabled: h.remoteDispatcher != nil, TaskControlsEnabled: h.remoteTaskController != nil, InspectionEnabled: h.remoteInspector != nil, Version: 1, Enabled: true, Digest: registry.Digest(), Registry: &registry})
	return true
}
