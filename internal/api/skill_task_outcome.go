package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/ArronJablonowski/NexusRouter/skills"
)

func (h *Handler) serveSkillTaskOutcome(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		failure(w, 405, "method_not_allowed")
		return
	}
	task := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/tasks/"), "/skill-outcome")
	if !replayTaskID(task) || r.ContentLength != 0 || len(r.TransferEncoding) != 0 || (r.Body != nil && r.Body != http.NoBody) || r.URL.RawQuery != "" || r.URL.ForceQuery {
		failure(w, 400, "invalid_request")
		return
	}
	if h.services.SkillTaskOutcome == nil {
		failure(w, 503, "skill_outcome_unavailable")
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
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if ctx.Err() != nil {
		failure(w, 503, "skill_outcome_unavailable")
		return
	}
	out, err := h.callSkillTaskOutcome(ctx, task)
	if err != nil || ctx.Err() != nil {
		failure(w, 503, "skill_outcome_unavailable")
		return
	}
	if out.TaskID != task || out.Validate() != nil {
		failure(w, 500, "invalid_skill_outcome")
		return
	}
	writeJSON(w, 200, out)
}

func (h *Handler) callSkillTaskOutcome(ctx context.Context, task string) (out skills.TaskOutcome, err error) {
	defer func() {
		if recover() != nil {
			out, err = skills.TaskOutcome{}, errors.New("skill outcome unavailable")
		}
	}()
	return h.services.SkillTaskOutcome(ctx, task)
}
