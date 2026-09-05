package api

import (
	"database/sql"
	"errors"
	"net/http"

	"darwinrouter/submissions"
)

func (h *Handler) submissionRecoveries(w http.ResponseWriter, r *http.Request, id string) {
	if !replayTaskID(id) {
		failure(w, 400, "invalid_submission_id")
		return
	}
	if h.services.SubmissionRecoveries == nil {
		failure(w, 503, "recoveries_unavailable")
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
	if r.Context().Err() != nil {
		return
	}
	history, err := h.services.SubmissionRecoveries(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			failure(w, 404, "submission_unavailable")
		} else {
			failure(w, 500, "recoveries_unavailable")
		}
		return
	}
	if len(history) > 4 {
		failure(w, 500, "invalid_recovery_history")
		return
	}
	seen := map[string]bool{}
	for _, record := range history {
		if record.Validate() != nil || record.SubmissionID != id || seen[record.ID] {
			failure(w, 500, "invalid_recovery_history")
			return
		}
		seen[record.ID] = true
	}
	if history == nil {
		history = []submissions.Recovery{}
	}
	writeJSON(w, 200, history)
}
