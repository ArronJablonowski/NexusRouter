package api

import (
	"context"
	"net/http"
	"time"

	"darwinrouter/health"
)

func (h *Handler) serveHealthReport(w http.ResponseWriter, r *http.Request) {
	if r.ContentLength > 0 {
		failure(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if h.services.HealthReport == nil {
		failure(w, http.StatusServiceUnavailable, "health_unavailable")
		return
	}
	select {
	case h.healthSlots <- struct{}{}:
		defer func() { <-h.healthSlots }()
	default:
		w.Header().Set("Retry-After", "1")
		failure(w, http.StatusServiceUnavailable, "capacity")
		return
	}
	// Leave room for the application's five-second probe budget to return a
	// typed degraded report instead of racing the endpoint deadline.
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	report, ok := h.callHealthReport(ctx)
	if !ok || ctx.Err() != nil || report.Validate() != nil {
		failure(w, http.StatusServiceUnavailable, "health_unavailable")
		return
	}
	status := http.StatusOK
	if !report.Ready {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, report)
}

// A failed diagnostic cannot expose provider errors or crash details. Keep the
// recovery boundary around the hook, not around response writes.
func (h *Handler) callHealthReport(ctx context.Context) (report health.Report, ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	var err error
	report, err = h.services.HealthReport(ctx)
	return report, err == nil
}
