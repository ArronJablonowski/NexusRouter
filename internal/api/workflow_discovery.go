package api

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/sessions"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

func (h *Handler) serveWorkflowDiscovery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || r.URL.Path != "/v1/skills/workflows" {
		failure(w, 404, "not_found")
		return
	}
	if len(r.URL.RawQuery) > 1024 {
		failure(w, 400, "invalid_request")
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		failure(w, 400, "invalid_request")
		return
	}
	for key, values := range query {
		if (key != "domain" && key != "after" && key != "scan_limit") || len(values) != 1 || values[0] == "" {
			failure(w, 400, "invalid_request")
			return
		}
	}
	domain, after := query.Get("domain"), query.Get("after")
	if !skillGenerationIdentifier.MatchString(domain) || (after != "" && !sessions.ValidEventPageID(after)) {
		failure(w, 400, "invalid_request")
		return
	}
	limit := 20
	if raw, ok := query["scan_limit"]; ok {
		limit, err = strconv.Atoi(raw[0])
		if err != nil || limit < 1 || limit > 20 || strconv.Itoa(limit) != raw[0] {
			failure(w, 400, "invalid_request")
			return
		}
	}
	if h.services.DiscoverSkillWorkflows == nil {
		failure(w, 503, "workflow_discovery_unavailable")
		return
	}
	release, ok := h.summaryPermit(w, r)
	if !ok {
		return
	}
	defer release()
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if ctx.Err() != nil {
		failure(w, 422, "admission_denied")
		return
	}
	page, err := callSkillGeneration(func() (skills.WorkflowCandidatePage, error) {
		return h.services.DiscoverSkillWorkflows(ctx, domain, after, limit)
	})
	if err != nil || ctx.Err() != nil {
		failure(w, 422, "admission_denied")
		return
	}
	if page.Validate(after, limit) != nil || page.Domain != domain {
		failure(w, 500, "invalid_service_result")
		return
	}
	writeJSON(w, 200, page)
}
