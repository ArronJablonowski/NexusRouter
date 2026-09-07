package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/routing"
)

func (h *Handler) serveConfiguredModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		failure(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if r.ContentLength != 0 || len(r.TransferEncoding) != 0 || (r.Body != nil && r.Body != http.NoBody) || r.URL.RawQuery != "" || r.URL.ForceQuery {
		failure(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if h.services.ConfiguredModels == nil {
		failure(w, http.StatusServiceUnavailable, "model_catalog_unavailable")
		return
	}
	select {
	case h.modelSlots <- struct{}{}:
		defer func() { <-h.modelSlots }()
	default:
		w.Header().Set("Retry-After", "1")
		failure(w, http.StatusServiceUnavailable, "model_catalog_capacity")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	catalog, err := h.callConfiguredModels(ctx)
	if err != nil || ctx.Err() != nil {
		failure(w, http.StatusServiceUnavailable, "model_catalog_unavailable")
		return
	}
	if catalog.Validate() != nil {
		failure(w, http.StatusInternalServerError, "invalid_model_catalog")
		return
	}
	catalog = cloneModelCatalog(catalog)
	writeJSON(w, http.StatusOK, catalog)
}

func (h *Handler) callConfiguredModels(ctx context.Context) (catalog routing.ModelCatalog, err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("model catalog unavailable")
		}
	}()
	return h.services.ConfiguredModels(ctx)
}

func cloneModelCatalog(catalog routing.ModelCatalog) routing.ModelCatalog {
	models := make([]routing.ConfiguredModel, len(catalog.Models))
	copy(models, catalog.Models)
	catalog.Models = models
	for i := range catalog.Models {
		catalog.Models[i].Capabilities = append([]string(nil), catalog.Models[i].Capabilities...)
		if catalog.Models[i].EstimatedCost != nil {
			value := *catalog.Models[i].EstimatedCost
			catalog.Models[i].EstimatedCost = &value
		}
	}
	return catalog
}
