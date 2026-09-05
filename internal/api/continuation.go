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

// This is a journal eligibility diagnostic, not an authorization or a provider
// readiness check. It never resumes the task or returns conversation content.
func (h *Handler) serveTaskContinuation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		failure(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	task := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/tasks/"), "/continuation")
	if !replayTaskID(task) {
		failure(w, 400, "invalid_task_id")
		return
	}
	if r.ContentLength != 0 || len(r.TransferEncoding) != 0 || (r.Body != nil && r.Body != http.NoBody) || r.URL.RawQuery != "" || r.URL.ForceQuery {
		failure(w, 400, "invalid_request")
		return
	}
	if h.services.TaskContinuation == nil {
		failure(w, 503, "continuation_unavailable")
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
		failure(w, 503, "continuation_unavailable")
		return
	}
	status, err := h.callTaskContinuation(ctx, task)
	if ctx.Err() != nil {
		failure(w, 503, "continuation_unavailable")
		return
	}
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			failure(w, 404, "task_unavailable")
		} else {
			failure(w, 500, "continuation_unavailable")
		}
		return
	}
	if status.TaskID != task || status.Sequence > 10000 || status.Validate() != nil {
		failure(w, 500, "invalid_continuation_status")
		return
	}
	writeJSON(w, 200, status)
}

func (h *Handler) callTaskContinuation(ctx context.Context, task string) (status sessions.ContinuationStatus, err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("continuation unavailable")
		}
	}()
	return h.services.TaskContinuation(ctx, task)
}
