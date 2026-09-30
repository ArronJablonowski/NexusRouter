package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"mime"
	"net/http"
	"strings"

	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
)

func (h *Handler) serveFeedback(w http.ResponseWriter, r *http.Request) {
	if h.services.Feedback == nil {
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
		w.Header().Set("Retry-After", "1")
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
	task, accepted, cost, err := decodeFeedback(body)
	if err != nil {
		failure(w, 400, "invalid_request")
		return
	}
	if err = h.services.Feedback(r.Context(), task, accepted, cost); err != nil {
		status, code := 500, "feedback_failed"
		switch {
		case errors.Is(err, telemetry.ErrConflict):
			status, code = 409, "feedback_conflict"
		case errors.Is(err, app.ErrAdmission):
			status, code = 422, "admission_denied"
		}
		failure(w, status, code)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok", "task_id": task})
}

func decodeFeedback(body []byte) (string, bool, float64, error) {
	bad := errors.New("invalid feedback")
	fields, err := chatObject(body, "task_id", "outcome", "attempt_cost")
	var task, outcome string
	if err != nil || chatString(fields["task_id"], &task) != nil || strings.TrimSpace(task) == "" || len(task) > 128 || strings.Contains(task, "/") || chatString(fields["outcome"], &outcome) != nil || (outcome != "accepted" && outcome != "rejected") {
		return "", false, 0, bad
	}
	var cost float64
	raw := bytes.TrimSpace(fields["attempt_cost"])
	if len(raw) == 0 || string(raw) == "null" || json.Unmarshal(raw, &cost) != nil || math.IsInf(cost, 0) || math.IsNaN(cost) || cost < 0 {
		return "", false, 0, bad
	}
	return task, outcome == "accepted", cost, nil
}
