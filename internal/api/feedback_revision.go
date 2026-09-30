package api

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
)

func (h *Handler) serveFeedbackHistory(w http.ResponseWriter, r *http.Request) {
	task := strings.TrimPrefix(r.URL.Path, "/v1/feedback/")
	if !feedbackID(task) {
		failure(w, 400, "invalid_task_id")
		return
	}
	if h.services.FeedbackHistory == nil {
		failure(w, 503, "feedback_unavailable")
		return
	}
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		failure(w, 503, "capacity")
		return
	}
	history, err := h.services.FeedbackHistory(r.Context(), task)
	if err != nil {
		failure(w, 404, "feedback_unavailable")
		return
	}
	writeJSON(w, 200, history)
}

func (h *Handler) serveFeedbackRevision(w http.ResponseWriter, r *http.Request) {
	if h.services.ReviseFeedback == nil {
		failure(w, 503, "feedback_unavailable")
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		failure(w, 415, "json_required")
		return
	}
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		failure(w, 503, "capacity")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<10))
	if err != nil {
		status := 400
		var limit *http.MaxBytesError
		if errors.As(err, &limit) {
			status = 413
		}
		failure(w, status, "invalid_request")
		return
	}
	fields, err := chatObject(body, "task_id", "expected_id", "outcome")
	var task, expected, outcome string
	if err != nil || chatString(fields["task_id"], &task) != nil || !feedbackID(task) || chatString(fields["expected_id"], &expected) != nil || !feedbackID(expected) || chatString(fields["outcome"], &outcome) != nil || (outcome != "accepted" && outcome != "rejected") {
		failure(w, 400, "invalid_request")
		return
	}
	if err = h.services.ReviseFeedback(r.Context(), task, expected, outcome == "accepted"); err != nil {
		status, code := 500, "feedback_failed"
		if errors.Is(err, telemetry.ErrConflict) {
			status, code = 409, "feedback_conflict"
		} else if errors.Is(err, app.ErrAdmission) {
			status, code = 422, "admission_denied"
		}
		failure(w, status, code)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok", "task_id": task})
}

func feedbackID(id string) bool {
	return id != "" && len(id) <= 128 && strings.TrimSpace(id) == id && !strings.Contains(id, "/")
}
