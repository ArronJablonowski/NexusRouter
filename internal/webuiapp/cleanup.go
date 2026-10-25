package webuiapp

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/maintenance"
)

func (h *Handler) serveCleanup(w http.ResponseWriter, r *http.Request) {
	action, token := "status", ""
	if r.Method == http.MethodGet {
		if !h.authenticated(r) {
			h.writeError(w, r, 401, "unauthorized")
			return
		}
		if !strictBrowserGET(r) || r.URL.RawQuery != "" {
			h.writeError(w, r, 400, "invalid_request")
			return
		}
	} else {
		if !h.requireMutationAuthority(w, r) {
			return
		}
		var input struct {
			Version int    `json:"version"`
			Action  string `json:"action"`
			Token   string `json:"token"`
		}
		if r.Method != http.MethodPost || decodeMutationJSON(r, &input, 4096) != nil || input.Version != 1 || (input.Action != "preview" && input.Action != "run") || (input.Action == "preview" && input.Token != "") || (input.Action == "run" && len(input.Token) != 64) {
			h.writeError(w, r, 400, "invalid_request")
			return
		}
		if input.Action == "run" {
			if _, err := hex.DecodeString(input.Token); err != nil {
				h.writeError(w, r, 400, "invalid_request")
				return
			}
		}
		action, token = input.Action, input.Token
	}
	select {
	case h.controlSlots <- struct{}{}:
		defer func() { <-h.controlSlots }()
	default:
		h.writeError(w, r, 503, "browser_capacity")
		return
	}
	if h.mutations.Cleanup == nil {
		h.writeError(w, r, 503, "cleanup_unavailable")
		return
	}
	// Once authorized, finish a bounded cleanup even if the browser disconnects.
	// The shared utility lock and journal prevent overlapping/replayed deletion.
	parent := r.Context()
	if action == "run" {
		parent = context.WithoutCancel(parent)
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()
	out, err := safeCall(func() (maintenance.Report, error) { return h.mutations.Cleanup(ctx, action, token) })
	if errors.Is(err, maintenance.ErrChanged) {
		h.writeError(w, r, 409, "cleanup_preview_changed")
		return
	}
	if err != nil || out.Version != 1 {
		h.writeError(w, r, 503, "cleanup_unavailable")
		return
	}
	h.writeJSON(w, 200, out)
}
