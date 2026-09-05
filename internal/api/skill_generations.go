package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var skillGenerationIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

func skillGenerationRoute(path string) bool {
	return path == "/v1/skills/generations" || strings.HasPrefix(path, "/v1/skills/generations/")
}

func (h *Handler) serveSkillGenerations(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		h.serveSkillGenerationAction(w, r)
		return
	}
	if r.Method != http.MethodGet {
		failure(w, 404, "not_found")
		return
	}
	list := r.URL.Path == "/v1/skills/generations"
	id := strings.TrimPrefix(r.URL.Path, "/v1/skills/generations/")
	if len(r.URL.RawQuery) > 1024 {
		failure(w, 400, "invalid_request")
		return
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	valid := err == nil && skillGenerationIdentifier.MatchString(q.Get("scope"))
	for k, values := range q {
		if len(values) != 1 || (k != "scope" && (!list || (k != "after" && k != "limit"))) {
			valid = false
		}
	}
	limit := 25
	if values, ok := q["limit"]; ok {
		limit, err = strconv.Atoi(values[0])
		valid = valid && err == nil && limit >= 1 && limit <= 100
	}
	after := q.Get("after")
	if (after != "" && !skillGenerationIdentifier.MatchString(after)) || (!list && !skillGenerationIdentifier.MatchString(id)) {
		valid = false
	}
	if !valid {
		failure(w, 400, "invalid_request")
		return
	}
	if (list && h.services.SkillGenerations == nil) || (!list && h.services.SkillGeneration == nil) {
		failure(w, 503, "skill_generation_unavailable")
		return
	}
	release, ok := h.summaryPermit(w, r)
	if !ok {
		return
	}
	defer release()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if list {
		items, err := h.services.SkillGenerations(ctx, q.Get("scope"), after, limit)
		if err != nil || ctx.Err() != nil {
			failure(w, 404, "skill_generation_unavailable")
			return
		}
		previous := after
		if items == nil || len(items) > limit {
			failure(w, 404, "skill_generation_unavailable")
			return
		}
		for _, item := range items {
			if item.Validate() != nil || item.ID <= previous || item.Key.Scope != q.Get("scope") {
				failure(w, 404, "skill_generation_unavailable")
				return
			}
			previous = item.ID
		}
		writeJSON(w, 200, items)
		return
	}
	item, err := h.services.SkillGeneration(ctx, q.Get("scope"), id)
	if err != nil || ctx.Err() != nil || item.Validate() != nil || item.ID != id || item.Key.Scope != q.Get("scope") {
		failure(w, 404, "skill_generation_unavailable")
		return
	}
	encoded, err := json.Marshal(item)
	if err != nil || len(encoded) > 512<<10 {
		failure(w, 404, "skill_generation_unavailable")
		return
	}
	writeJSON(w, 200, item)
}
