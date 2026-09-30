package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

// serveTask creates work only through the durable idempotent submission
// journal. A disconnected caller may repeat the exact key and body without
// dispatching another model or tool execution.
func (h *Handler) serveTask(w http.ResponseWriter, r *http.Request) {
	if h.services.RunSubmission == nil {
		failure(w, http.StatusServiceUnavailable, "durable_tasks_unavailable")
		return
	}
	key, ok := submissionKey(r.Header)
	if !ok {
		failure(w, http.StatusBadRequest, "invalid_idempotency_key")
		return
	}
	if !submissionJSONMedia(w, r) {
		return
	}
	// Bound synchronous waiters independently from detached submission intake.
	// Once admitted, the daemon dispatcher owns the work even if this HTTP
	// request disappears.
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		w.Header().Set("Retry-After", "1")
		failure(w, http.StatusServiceUnavailable, "capacity")
		return
	}
	req, err := decodeRequest(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		failure(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if r.Context().Err() != nil {
		return
	}
	status, err := h.services.RunSubmission(r.Context(), key, req)
	if err != nil {
		if errors.Is(err, submissions.ErrConflict) {
			failure(w, http.StatusConflict, "task_conflict")
			return
		}
		if errors.Is(err, submissions.ErrCapacity) {
			w.Header().Set("Retry-After", "1")
			failure(w, http.StatusServiceUnavailable, "submission_capacity")
			return
		}
		if errors.Is(err, app.ErrAdmission) {
			failure(w, http.StatusUnprocessableEntity, "admission_denied")
			return
		}
		if errors.Is(err, context.DeadlineExceeded) && (status.State == "queued" || status.State == "running") && validSubmissionStatus(status, "") {
			w.Header().Set("Retry-After", "1")
			writeJSON(w, http.StatusAccepted, map[string]any{"submission_id": status.ID, "state": status.State, "task_ids": status.TaskIDs})
			return
		}
		// A canceled wait does not cancel the durable submission. There may be no
		// usable writer left, so avoid inventing a terminal task response.
		if r.Context().Err() != nil {
			return
		}
		failure(w, http.StatusInternalServerError, "task_unavailable")
		return
	}
	if !terminalSubmission(status.State) || !validSubmissionStatus(status, "") {
		failure(w, http.StatusInternalServerError, "invalid_submission_status")
		return
	}
	h.writeTaskSubmission(w, status)
}

func terminalSubmission(state string) bool {
	return state == "succeeded" || state == "failed" || state == "canceled"
}

func (h *Handler) writeTaskSubmission(w http.ResponseWriter, status submissions.Status) {
	base := map[string]any{
		"submission_id": status.ID,
		"task_ids":      status.TaskIDs,
	}
	if status.State == "succeeded" && status.Result != nil {
		result := status.Result
		base["task_id"] = result.TaskID
		base["text"] = result.Text
		base["turns"] = result.Turns
		base["finish_reason"] = result.FinishReason
		base["usage"] = result.Usage
		base["audit_id"] = result.AuditID
		base["audit_status"] = result.AuditStatus
		base["previous_task_ids"] = result.PreviousTaskIDs
		base["route_estimated_cost"] = result.RouteEstimatedCost
		writeJSON(w, http.StatusCreated, base)
		return
	}
	base["error"] = status.ErrorCode
	if status.Result != nil {
		base["task_id"] = status.Result.TaskID
		base["previous_task_ids"] = status.Result.PreviousTaskIDs
		base["route_estimated_cost"] = status.Result.RouteEstimatedCost
	}
	code := http.StatusInternalServerError
	if status.State == "canceled" {
		code = http.StatusConflict
	} else if status.ErrorCode == "admission_denied" {
		code = http.StatusUnprocessableEntity
	}
	writeJSON(w, code, base)
}
