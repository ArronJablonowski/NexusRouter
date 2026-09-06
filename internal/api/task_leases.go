package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/workers"
)

// Task lease diagnostics contain aggregate durable metadata only. They never
// confer lease ownership, probe a process guard, repair state or dispatch work.
func (h *Handler) serveTaskLeases(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		failure(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	task := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/tasks/"), "/leases")
	if !replayTaskID(task) {
		failure(w, 400, "invalid_task_id")
		return
	}
	if r.ContentLength != 0 || len(r.TransferEncoding) != 0 || (r.Body != nil && r.Body != http.NoBody) || r.URL.RawQuery != "" || r.URL.ForceQuery {
		failure(w, 400, "invalid_request")
		return
	}
	if h.services.TaskLeases == nil {
		failure(w, 503, "task_leases_unavailable")
		return
	}
	select {
	case h.controls <- struct{}{}:
		defer func() { <-h.controls }()
	default:
		w.Header().Set("Retry-After", "1")
		failure(w, 503, "control_capacity")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if ctx.Err() != nil {
		failure(w, 503, "task_leases_unavailable")
		return
	}
	status, err := h.callTaskLeases(ctx, task)
	if ctx.Err() != nil {
		failure(w, 503, "task_leases_unavailable")
		return
	}
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			failure(w, 404, "task_unavailable")
		} else {
			failure(w, 503, "task_leases_unavailable")
		}
		return
	}
	if status.TaskID != task || status.Validate() != nil {
		failure(w, 500, "invalid_task_leases")
		return
	}
	writeJSON(w, 200, status)
}

func (h *Handler) callTaskLeases(ctx context.Context, task string) (status workers.TaskLeaseStatus, err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("task leases unavailable")
		}
	}()
	return h.services.TaskLeases(ctx, task)
}
