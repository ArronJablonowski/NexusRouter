package webuiapp

import (
	"context"
	"errors"
	"github.com/ArronJablonowski/NexusRouter/internal/maintenance"
	"net/http"
	"time"
)

func (h *Handler) serveCleanupSchedule(w http.ResponseWriter, r *http.Request) {
	var input *maintenance.ScheduleUpdate
	if r.Method == http.MethodGet {
		if !h.authenticated(r) {
			h.writeError(w, r, 401, "unauthorized")
			return
		}
		if !strictBrowserGET(r) || r.URL.RawQuery != "" {
			h.writeError(w, r, 400, "invalid_request")
			return
		}
	} else {
		if !h.requireMutationAuthority(w, r) {
			return
		}
		input = &maintenance.ScheduleUpdate{}
		if r.Method != http.MethodPost || decodeMutationJSON(r, input, 4096) != nil || !input.Valid() {
			h.writeError(w, r, 400, "invalid_cleanup_schedule")
			return
		}
	}
	select {
	case h.controlSlots <- struct{}{}:
		defer func() { <-h.controlSlots }()
	default:
		h.writeError(w, r, 503, "browser_capacity")
		return
	}
	if h.mutations.CleanupSchedule == nil {
		h.writeError(w, r, 503, "cleanup_schedule_unavailable")
		return
	}
	parent := r.Context()
	if input != nil {
		parent = context.WithoutCancel(parent)
	}
	ctx, cancel := context.WithTimeout(parent, 90*time.Second)
	defer cancel()
	out, err := safeCall(func() (maintenance.ScheduleReport, error) { return h.mutations.CleanupSchedule(ctx, input) })
	if errors.Is(err, maintenance.ErrScheduleChanged) {
		h.writeError(w, r, 409, "cleanup_schedule_changed")
		return
	}
	if err != nil || out.Version != 1 {
		h.writeError(w, r, 503, "cleanup_schedule_unavailable")
		return
	}
	h.writeJSON(w, 200, out)
}
