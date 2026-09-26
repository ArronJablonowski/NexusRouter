package webuiapp

import (
	"context"
	"net/http"
	"time"
)

func (h *Handler) serveSkills(w http.ResponseWriter, r *http.Request) {
	if !h.prevalidateInspectionGET(w, r) {
		return
	}
	if h.inspections.Skills == nil {
		h.writeError(w, r, 503, "skills_unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	p, err := h.inspections.Skills(ctx)
	if err != nil {
		h.writeError(w, r, 503, "skills_unavailable")
		return
	}
	h.writeJSON(w, 200, p)
}
