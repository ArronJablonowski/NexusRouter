package api

import (
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"unicode/utf8"

	"darwinrouter/internal/app"
	"darwinrouter/submissions"
)

// Detached intake has independent capacity so active tasks cannot prevent
// callers from submitting durable work or inspecting/canceling queued work.
func (h *Handler) serveSubmissions(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/submissions" {
		if r.Method == http.MethodGet {
			h.listSubmissions(w, r)
			return
		}
		if r.Method != http.MethodPost {
			failure(w, 404, "not_found")
			return
		}
		h.submit(w, r)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/v1/submissions/")
	if r.Method == http.MethodGet && strings.HasSuffix(id, "/recoveries") {
		h.submissionRecoveries(w, r, strings.TrimSuffix(id, "/recoveries"))
		return
	}
	mutating := r.Method == http.MethodPost && strings.HasSuffix(id, "/cancel")
	if mutating {
		id = strings.TrimSuffix(id, "/cancel")
	} else if r.Method != http.MethodGet {
		failure(w, 404, "not_found")
		return
	}
	if !replayTaskID(id) {
		failure(w, 400, "invalid_submission_id")
		return
	}
	hook := h.services.Submission
	if mutating {
		hook = h.services.CancelSubmission
	}
	if hook == nil {
		failure(w, 503, "submissions_unavailable")
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
	if mutating && !submissionEmptyBody(w, r) {
		return
	}
	if r.Context().Err() != nil {
		return
	}
	status, err := hook(r.Context(), id)
	if err != nil {
		submissionFailure(w, err)
		return
	}
	if !validSubmissionStatus(status, id) {
		failure(w, 500, "invalid_submission_status")
		return
	}
	code := 200
	if mutating && (status.State == "queued" || status.State == "running") {
		code = 202
	}
	writeJSON(w, code, status)
}

func (h *Handler) submit(w http.ResponseWriter, r *http.Request) {
	if h.services.Submit == nil {
		failure(w, 503, "submissions_unavailable")
		return
	}
	key, ok := submissionKey(r.Header)
	if !ok {
		failure(w, 400, "invalid_idempotency_key")
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
		failure(w, 503, "intake_capacity")
		return
	}
	request, err := decodeRequest(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		failure(w, 400, "invalid_request")
		return
	}
	if r.Context().Err() != nil {
		return
	}
	status, err := h.services.Submit(r.Context(), key, request)
	if err != nil {
		submissionFailure(w, err)
		return
	}
	if !validSubmissionStatus(status, "") {
		failure(w, 500, "invalid_submission_status")
		return
	}
	code := 200
	if status.State == "queued" || status.State == "running" {
		code = 202
	}
	writeJSON(w, code, status)
}

func submissionKey(header http.Header) (string, bool) {
	var values []string
	for key, items := range header {
		if strings.EqualFold(key, "Idempotency-Key") {
			if values != nil || len(items) != 1 {
				return "", false
			}
			values = items
		}
	}
	if len(values) != 1 || len(values[0]) < 16 || len(values[0]) > 128 {
		return "", false
	}
	for _, c := range values[0] {
		if c < 33 || c > 126 {
			return "", false
		}
	}
	return values[0], true
}

func submissionJSONMedia(w http.ResponseWriter, r *http.Request) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		failure(w, 415, "json_required")
		return false
	}
	return true
}

func submissionEmptyBody(w http.ResponseWriter, r *http.Request) bool {
	if !submissionJSONMedia(w, r) {
		return false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024))
	if err != nil {
		code := 400
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			code = 413
		}
		failure(w, code, "invalid_request")
		return false
	}
	fields, err := chatObject(body)
	if err != nil || len(fields) != 0 {
		failure(w, 400, "invalid_request")
		return false
	}
	return true
}

func submissionFailure(w http.ResponseWriter, err error) {
	status, code := 500, "submission_unavailable"
	switch {
	case errors.Is(err, sql.ErrNoRows):
		status, code = 404, "submission_unavailable"
	case errors.Is(err, submissions.ErrConflict):
		status, code = 409, "submission_conflict"
	case errors.Is(err, submissions.ErrCapacity):
		status, code = 503, "submission_capacity"
		w.Header().Set("Retry-After", "1")
	case errors.Is(err, submissions.ErrInvalid):
		status, code = 400, "invalid_submission"
	case errors.Is(err, app.ErrAdmission):
		status, code = 422, "admission_denied"
	}
	failure(w, status, code)
}

func validSubmissionStatus(s submissions.Status, expectedID string) bool {
	digest, err := hex.DecodeString(s.ConfigDigest)
	if s.Version != 1 || !replayTaskID(s.ID) || expectedID != "" && s.ID != expectedID || s.CreatedAt.IsZero() || s.UpdatedAt.IsZero() || s.UpdatedAt.Before(s.CreatedAt) || err != nil || len(digest) != 32 || strings.ToLower(s.ConfigDigest) != s.ConfigDigest {
		return false
	}
	if s.LeaseExpiresAt != nil && s.LeaseExpiresAt.IsZero() || s.LeaseExpired && (s.State != "running" || s.LeaseExpiresAt == nil) {
		return false
	}
	known := map[string]bool{}
	for _, task := range s.TaskIDs {
		if !replayTaskID(task) || known[task] {
			return false
		}
		known[task] = true
	}
	switch s.State {
	case "queued", "running":
		return s.Result == nil && s.ErrorCode == ""
	case "succeeded":
		if s.Result == nil || s.ErrorCode != "" || !known[s.Result.TaskID] || s.Result.Turns < 1 {
			return false
		}
		return validSubmissionResult(s.Result, known)
	case "failed", "canceled":
		if s.Result != nil && (s.Result.Text != "" || !validSubmissionResult(s.Result, known)) {
			return false
		}
		// Canceling queued work has no execution failure to record.
		if s.State == "canceled" && s.CancelRequested && s.ErrorCode == "" {
			return true
		}
		switch s.ErrorCode {
		case "task_failed", "execution_failed", "canceled", "interrupted", "lease_lost", "admission_denied", "deadline_exceeded", "persistence_failed", "recovery_exhausted":
			return true
		}
	}
	return false
}

func validSubmissionResult(result *submissions.Result, tasks map[string]bool) bool {
	if result.TaskID != "" && !tasks[result.TaskID] || result.Turns < 0 || !utf8.ValidString(result.Text) {
		return false
	}
	for _, prior := range result.PreviousTaskIDs {
		if !tasks[prior] {
			return false
		}
	}
	return result.Usage == nil || result.Usage.InputTokens >= 0 && result.Usage.OutputTokens >= 0
}
