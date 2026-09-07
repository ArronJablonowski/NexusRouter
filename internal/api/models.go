package api

import (
	"net/http"
	"regexp"
)

var modelCatalogID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

type modelCatalogItem struct {
	ID           string  `json:"id"`
	Object       string  `json:"object"`
	Created      int64   `json:"created"`
	OwnedBy      string  `json:"owned_by"`
	ShutdownDate *string `json:"shutdown_date"`
}

func (h *Handler) serveModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		chatFailure(w, http.StatusMethodNotAllowed, "invalid_request_error", "method_not_allowed")
		return
	}
	if h.services.Models == nil {
		chatFailure(w, http.StatusNotImplemented, "server_error", "models_unavailable")
		return
	}
	select {
	case h.modelSlots <- struct{}{}:
		defer func() { <-h.modelSlots }()
	default:
		w.Header().Set("Retry-After", "1")
		chatFailure(w, http.StatusServiceUnavailable, "server_error", "capacity")
		return
	}
	ids, err := h.services.Models(r.Context())
	if err != nil || r.Context().Err() != nil || len(ids) > 256 {
		chatFailure(w, http.StatusServiceUnavailable, "server_error", "models_unavailable")
		return
	}
	seen := make(map[string]struct{}, len(ids))
	items := make([]modelCatalogItem, len(ids))
	for i, id := range ids {
		if !modelCatalogID.MatchString(id) {
			chatFailure(w, http.StatusServiceUnavailable, "server_error", "models_unavailable")
			return
		}
		if _, exists := seen[id]; exists {
			chatFailure(w, http.StatusServiceUnavailable, "server_error", "models_unavailable")
			return
		}
		seen[id] = struct{}{}
		items[i] = modelCatalogItem{ID: id, Object: "model", Created: 0, OwnedBy: "darwinrouter"}
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": items})
}
