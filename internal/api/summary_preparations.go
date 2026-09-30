package api

import (
	"database/sql"
	"errors"
	"math"
	"net/http"
	"strings"

	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

func (h *Handler) serveSummaryPreparations(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/summary-preparations" {
		if r.Method != http.MethodPost {
			failure(w, 404, "not_found")
			return
		}
		h.serveSummaryPreparation(w, r)
		return
	}
	if r.Method != http.MethodGet {
		failure(w, 404, "not_found")
		return
	}
	operationID := strings.TrimPrefix(r.URL.Path, "/v1/summary-preparations/")
	if !summaryID(operationID) {
		failure(w, 400, "invalid_summary_preparation_id")
		return
	}
	if h.services.SummaryPreparation == nil {
		failure(w, 503, "summary_preparations_unavailable")
		return
	}
	release, ok := h.summaryPermit(w, r)
	if !ok {
		return
	}
	defer release()
	state, err := h.services.SummaryPreparation(r.Context(), operationID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			failure(w, 404, "summary_preparation_unavailable")
		} else {
			failure(w, 500, "summary_preparation_failed")
		}
		return
	}
	if state.Validate() != nil || state.Start.OperationID != operationID {
		failure(w, 500, "invalid_summary_preparation")
		return
	}
	writeJSON(w, 200, state)
}

func (h *Handler) serveSummaryPreparation(w http.ResponseWriter, r *http.Request) {
	if h.services.PrepareSummary == nil {
		failure(w, 503, "summary_preparations_unavailable")
		return
	}
	key, ok := exactIdempotencyHeader(r)
	if !ok {
		failure(w, 400, "invalid_idempotency_key")
		return
	}
	release, ok := h.summaryPermit(w, r)
	if !ok {
		return
	}
	defer release()
	fields, ok := summaryBody(w, r, "task_id", "model_id", "keep", "max_cost")
	if !ok {
		return
	}
	request := app.PrepareSummaryRequest{Version: 1}
	if chatString(fields["task_id"], &request.TaskID) != nil || !summaryID(request.TaskID) ||
		chatString(fields["model_id"], &request.ModelID) != nil || !summaryID(request.ModelID) ||
		summaryNumber(fields["keep"], &request.Keep) != nil || request.Keep < 1 || request.Keep > 100000 ||
		summaryNumber(fields["max_cost"], &request.MaxCost) != nil || request.MaxCost < 0 || math.IsNaN(request.MaxCost) || math.IsInf(request.MaxCost, 0) {
		failure(w, 400, "invalid_request")
		return
	}
	state, err := h.services.PrepareSummary(r.Context(), key, request)
	if err != nil {
		switch {
		case errors.Is(err, telemetry.ErrConflict):
			failure(w, 409, "summary_preparation_conflict")
		case errors.Is(err, app.ErrAdmission), errors.Is(err, sessions.ErrHistory), errors.Is(err, sessions.ErrContextCompactionLifecycle):
			failure(w, 422, "admission_denied")
		default:
			failure(w, 500, "summary_preparation_failed")
		}
		return
	}
	if state.Validate() != nil {
		failure(w, 500, "invalid_summary_preparation")
		return
	}
	writeJSON(w, 200, state)
}
