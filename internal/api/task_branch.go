package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

func branchTaskID(path string) string {
	parts := strings.Split(strings.TrimPrefix(path, "/v1/tasks/"), "/")
	if !strings.HasPrefix(path, "/v1/tasks/") || len(parts) != 2 || parts[1] != "branches" || !replayTaskID(parts[0]) {
		return ""
	}
	return parts[0]
}

// serveTaskBranch durably queues one direct child of an exact completed task
// head. The source fence is content-free and must agree with the path.
func (h *Handler) serveTaskBranch(w http.ResponseWriter, r *http.Request, sourceTask string) {
	if r.Method != http.MethodPost {
		failure(w, http.StatusNotFound, "not_found")
		return
	}
	if h.services.SubmitBranch == nil {
		failure(w, http.StatusServiceUnavailable, "branches_unavailable")
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
	status, err := h.services.SubmitBranch(r.Context(), key, source, request)
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

func decodeBranchRequest(body []byte) (sessions.TaskHeadFence, app.Request, error) {
	bad := errors.New("invalid branch request")
	fields, err := chatObject(body, "version", "source", "request")
	if err != nil || len(fields) != 3 {
		return sessions.TaskHeadFence{}, app.Request{}, bad
	}
	var version int
	if json.Unmarshal(fields["version"], &version) != nil || version != 1 {
		return sessions.TaskHeadFence{}, app.Request{}, bad
	}
	sourceFields, err := chatObject(fields["source"], "version", "task_id", "session_id", "head_sequence", "head_event_id")
	if err != nil || len(sourceFields) != 5 {
		return sessions.TaskHeadFence{}, app.Request{}, bad
	}
	var source sessions.TaskHeadFence
	if json.Unmarshal(sourceFields["version"], &source.Version) != nil ||
		chatString(sourceFields["task_id"], &source.TaskID) != nil ||
		chatString(sourceFields["session_id"], &source.SessionID) != nil ||
		json.Unmarshal(sourceFields["head_sequence"], &source.HeadSequence) != nil ||
		chatString(sourceFields["head_event_id"], &source.HeadEventID) != nil || source.Validate() != nil {
		return sessions.TaskHeadFence{}, app.Request{}, bad
	}
	request, err := decodeRequest(bytes.NewReader(fields["request"]))
	if err != nil {
		return sessions.TaskHeadFence{}, app.Request{}, bad
	}
	return source, request, nil
}
