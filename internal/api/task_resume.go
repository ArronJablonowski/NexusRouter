package api

import (
	"errors"
	"io"
	"net/http"
	"strings"
)

func resumeTaskID(path string) string {
	parts := strings.Split(strings.TrimPrefix(path, "/v1/tasks/"), "/")
	if !strings.HasPrefix(path, "/v1/tasks/") || len(parts) != 2 || parts[1] != "resumes" || !replayTaskID(parts[0]) {
		return ""
	}
	return parts[0]
}

// serveTaskResume durably queues new work from an exact recovered-history
// source. The application replays the source and decides eligibility.
func (h *Handler) serveTaskResume(w http.ResponseWriter, r *http.Request, sourceTask string) {
	if r.Method != http.MethodPost {
		failure(w, http.StatusNotFound, "not_found")
		return
	}
	if h.services.SubmitResume == nil {
		failure(w, http.StatusServiceUnavailable, "resumes_unavailable")
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
	select {
	case h.intake <- struct{}{}:
		defer func() { <-h.intake }()
	default:
		w.Header().Set("Retry-After", "1")
		failure(w, http.StatusServiceUnavailable, "intake_capacity")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		status := http.StatusBadRequest
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		failure(w, status, "invalid_request")
		return
	}
	source, request, err := decodeBranchRequest(body)
	if err != nil || source.TaskID != sourceTask || request.ContinueTaskID != "" || request.Compaction != nil || request.SummaryAttemptID != "" {
		failure(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if r.Context().Err() != nil {
		return
	}
	status, err := h.services.SubmitResume(r.Context(), key, source, request)
	if err != nil {
		submissionFailure(w, err)
		return
	}
	if !validSubmissionStatus(status, "") {
		failure(w, http.StatusInternalServerError, "invalid_submission_status")
		return
	}
	code := http.StatusOK
	if status.State == "queued" || status.State == "running" {
		code = http.StatusAccepted
	}
	writeJSON(w, code, status)
}
