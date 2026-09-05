package api

import (
	"context"
	"net/http"

	"github.com/ArronJablonowski/DarwinRouter/metrics"
)

func (h *Handler) serveMetrics(w http.ResponseWriter, r *http.Request) {
	// Reject bodies using framing metadata, never by reading a slow stream.
	if r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		failure(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if h.services.Metrics == nil {
		failure(w, http.StatusServiceUnavailable, "metrics_unavailable")
		return
	}
	select {
	case h.metricsSlots <- struct{}{}:
		defer func() { <-h.metricsSlots }()
	default:
		w.Header().Set("Retry-After", "1")
		failure(w, http.StatusServiceUnavailable, "capacity")
		return
	}
	if r.Context().Err() != nil {
		failure(w, http.StatusServiceUnavailable, "metrics_unavailable")
		return
	}
	snapshot, ok := h.callMetrics(r.Context())
	if !ok || r.Context().Err() != nil || snapshot.Validate() != nil {
		failure(w, http.StatusServiceUnavailable, "metrics_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (h *Handler) callMetrics(ctx context.Context) (snapshot metrics.Snapshot, ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	var err error
	snapshot, err = h.services.Metrics(ctx)
	return snapshot, err == nil
}
