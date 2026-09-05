package api

import (
	"database/sql"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"darwinrouter/runtime"
)

// Cancellation has independent bounded capacity: occupied execution slots must
// never prevent a client from asking those tasks to stop.
func (h *Handler) serveCancellation(w http.ResponseWriter, r *http.Request) {
	suffix := "/cancellation"
	mutating := r.Method == http.MethodPost
	if mutating {
		suffix = "/cancel"
	}
	task := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/tasks/"), suffix)
	if !replayTaskID(task) {
		failure(w, 400, "invalid_task_id")
		return
	}
	hook := h.services.Cancellation
	if mutating {
		hook = h.services.Cancel
	}
	if hook == nil {
		failure(w, 503, "cancellation_unavailable")
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
	if mutating {
		media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || media != "application/json" {
			failure(w, 415, "json_required")
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024))
		if err != nil {
			status := 400
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				status = 413
			}
			failure(w, status, "invalid_request")
			return
		}
		fields, err := chatObject(body)
		if err != nil || len(fields) != 0 {
			failure(w, 400, "invalid_request")
			return
		}
	}
	if r.Context().Err() != nil {
		return
	}
	status, err := hook(r.Context(), task)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			failure(w, 404, "task_unavailable")
		} else {
			failure(w, 500, "cancellation_unavailable")
		}
		return
	}
	if !validCancellationStatus(task, status) {
		failure(w, 500, "invalid_cancellation_status")
		return
	}
	code := 200
	if mutating && status.Requested && status.State == "running" {
		code = 202
	}
	writeJSON(w, code, status)
}

func validCancellationStatus(task string, s runtime.CancellationStatus) bool {
	if s.Version != 1 || s.TaskID != task {
		return false
	}
	switch s.State {
	case "running", "completed", "failed", "canceled":
	default:
		return false
	}
	if s.Requested {
		return replayTaskID(s.RequestID) && s.RequestedAt != nil && !s.RequestedAt.IsZero()
	}
	return s.RequestID == "" && s.RequestedAt == nil
}
