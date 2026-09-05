package api

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/approvals"
	"github.com/ArronJablonowski/DarwinRouter/internal/app"
)

func (h *Handler) serveApprovalDecision(w http.ResponseWriter, r *http.Request, task, id string) {
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		failure(w, 400, "invalid_approval_request")
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		failure(w, 415, "json_required")
		return
	}
	if h.services.DecideApproval == nil {
		failure(w, 503, "approvals_unavailable")
		return
	}
	select {
	case h.approvalSlots <- struct{}{}:
		defer func() { <-h.approvalSlots }()
	default:
		w.Header().Set("Retry-After", "1")
		failure(w, 503, "approval_capacity")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if ctx.Err() != nil {
		failure(w, 503, "approvals_unavailable")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<10))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			failure(w, 413, "invalid_approval_request")
		} else {
			failure(w, 400, "invalid_approval_request")
		}
		return
	}
	command, err := approvals.ParseCommand(body)
	if err != nil || command.Validate() != nil || command.Expected.TaskID != task || command.Expected.ID != id {
		failure(w, 400, "invalid_approval_request")
		return
	}
	if ctx.Err() != nil {
		failure(w, 503, "approvals_unavailable")
		return
	}
	record, err := h.services.DecideApproval(ctx, command)
	if err != nil {
		switch {
		case errors.Is(err, approvals.ErrConflict):
			failure(w, 409, "approval_conflict")
		case errors.Is(err, approvals.ErrInvalid), errors.Is(err, app.ErrAdmission):
			failure(w, 400, "invalid_approval_request")
		default:
			failure(w, 503, "approvals_unavailable")
		}
		return
	}
	if ctx.Err() != nil {
		failure(w, 503, "approvals_unavailable")
		return
	}
	valid := record.Validate() == nil && record.Request.Matches(command.Expected)
	matched := false
	for _, d := range record.Decisions {
		if d.ID == command.ID && d.Allowed == command.Allowed {
			matched = true
		}
	}
	if !valid || !matched {
		failure(w, 500, "invalid_approval_record")
		return
	}
	// A retry may return a later revoked/consumed state. It never dispatches work.
	writeJSON(w, 200, record)
}
