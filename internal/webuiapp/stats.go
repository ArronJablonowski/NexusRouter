package webuiapp

import (
	"context"
	"errors"
	"github.com/ArronJablonowski/DarwinRouter/internal/usagestats"
	"net/http"
	"time"
)

func (h *Handler) serveStats(w http.ResponseWriter, r *http.Request) {
	var reset *usagestats.Reset
	if r.Method == http.MethodGet {
		if !h.prevalidateInspectionGET(w, r) {
			return
		}
	} else if r.Method == http.MethodPost {
		if !h.authorizedMutation(r) {
			h.writeError(w, r, http.StatusForbidden, "reset_denied")
			return
		}
		var input usagestats.Reset
		if decodeBrowserJSON(r, &input) != nil || !input.Valid() {
			h.writeError(w, r, http.StatusBadRequest, "invalid_reset")
			return
		}
		reset = &input
	} else {
		w.Header().Set("Allow", "GET, POST")
		h.writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if h.inspections.Stats == nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "stats_unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	value, err := h.inspections.Stats(ctx, reset)
	if errors.Is(err, usagestats.ErrConflict) {
		h.writeError(w, r, http.StatusConflict, "trip_changed")
		return
	}
	if err != nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "stats_unavailable")
		return
	}
	h.writeJSON(w, http.StatusOK, value)
}
