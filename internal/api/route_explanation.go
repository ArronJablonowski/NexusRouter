package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func (h *Handler) serveRouteExplanation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		failure(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	task := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/tasks/"), "/route")
	if !replayTaskID(task) {
		failure(w, http.StatusBadRequest, "invalid_task_id")
		return
	}
	if r.ContentLength != 0 || len(r.TransferEncoding) != 0 || (r.Body != nil && r.Body != http.NoBody) || r.URL.RawQuery != "" || r.URL.ForceQuery {
		failure(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if h.services.RouteExplanation == nil {
		failure(w, http.StatusServiceUnavailable, "route_unavailable")
		return
	}
	select {
	case h.controls <- struct{}{}:
		defer func() { <-h.controls }()
	default:
		w.Header().Set("Retry-After", "1")
		failure(w, http.StatusServiceUnavailable, "control_capacity")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	out, err := h.callRouteExplanation(ctx, task)
	if ctx.Err() != nil {
		failure(w, http.StatusServiceUnavailable, "route_unavailable")
		return
	}
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, sessions.ErrRouteExplanation) {
			failure(w, http.StatusNotFound, "route_unavailable")
		} else {
			failure(w, http.StatusInternalServerError, "route_unavailable")
		}
		return
	}
	if out.TaskID != task || out.Validate() != nil {
		failure(w, http.StatusInternalServerError, "invalid_route_explanation")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) callRouteExplanation(ctx context.Context, task string) (out sessions.RouteExplanation, err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("route explanation unavailable")
		}
	}()
	return h.services.RouteExplanation(ctx, task)
}
