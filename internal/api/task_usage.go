package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/accounting"
)

func (h *Handler) serveTaskUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		failure(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	task := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/tasks/"), "/usage")
	if !replayTaskID(task) {
		failure(w, http.StatusBadRequest, "invalid_task_id")
		return
	}
	if r.ContentLength != 0 || len(r.TransferEncoding) != 0 || (r.Body != nil && r.Body != http.NoBody) || r.URL.RawQuery != "" || r.URL.ForceQuery {
		failure(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if h.services.TaskUsage == nil {
		failure(w, http.StatusServiceUnavailable, "usage_unavailable")
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
	totals, err := h.callTaskUsage(ctx, task)
	if ctx.Err() != nil {
		failure(w, http.StatusServiceUnavailable, "usage_unavailable")
		return
	}
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			failure(w, http.StatusNotFound, "usage_unavailable")
		} else {
			failure(w, http.StatusInternalServerError, "usage_unavailable")
		}
		return
	}
	if totals.Validate() != nil || totals.Scope.TaskID != task || totals.Scope.SessionID == "" || usageContainsSecret(totals, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")) {
		failure(w, http.StatusInternalServerError, "invalid_usage_totals")
		return
	}
	writeJSON(w, http.StatusOK, totals)
}

func (h *Handler) callTaskUsage(ctx context.Context, task string) (totals accounting.Totals, err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("usage unavailable")
		}
	}()
	return h.services.TaskUsage(ctx, task)
}

func usageContainsSecret(totals accounting.Totals, secret string) bool {
	if secret == "" {
		return false
	}
	return strings.Contains(totals.Scope.TaskID, secret) || strings.Contains(totals.Scope.SessionID, secret)
}
