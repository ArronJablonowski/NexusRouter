package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/ArronJablonowski/NexusRouter/skills"
)

var skillGenerationModelID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

func (h *Handler) serveSkillGenerationAction(w http.ResponseWriter, r *http.Request) {
	generate := r.URL.Path == "/v1/skills/generations"
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/skills/generations/"), "/publish")
	if r.Method != http.MethodPost || (!generate && (!strings.HasSuffix(r.URL.Path, "/publish") || !skillGenerationIdentifier.MatchString(id))) {
		failure(w, 404, "not_found")
		return
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		failure(w, 400, "invalid_request")
		return
	}
	if (generate && h.services.GenerateSkillDraft == nil) || (!generate && h.services.PublishSkillGeneration == nil) {
		failure(w, 503, "skill_generation_unavailable")
		return
	}
	release, ok := h.summaryPermit(w, r)
	if !ok {
		return
	}
	defer release()
	keys := []string{"version"}
	if generate {
		keys = append(keys, "id", "model_id", "name", "task_ids", "max_cost")
	}
	fields, ok := summaryBody(w, r, keys...)
	if !ok {
		return
	}
	var version int
	if summaryNumber(fields["version"], &version) != nil || version != 1 {
		failure(w, 400, "invalid_request")
		return
	}
	var model, name string
	var tasks []string
	cost := 0.0
	if generate {
		if chatString(fields["id"], &id) != nil || !skillGenerationIdentifier.MatchString(id) || chatString(fields["model_id"], &model) != nil || !skillGenerationModelID.MatchString(model) || chatString(fields["name"], &name) != nil || !skillGenerationIdentifier.MatchString(name) {
			failure(w, 400, "invalid_request")
			return
		}
		raw := bytes.TrimSpace(fields["task_ids"])
		if len(raw) == 0 || raw[0] != '[' || json.Unmarshal(raw, &tasks) != nil || len(tasks) < 2 || len(tasks) > 20 {
			failure(w, 400, "invalid_request")
			return
		}
		seen := map[string]bool{}
		for _, task := range tasks {
			if !skillGenerationIdentifier.MatchString(task) || seen[task] {
				failure(w, 400, "invalid_request")
				return
			}
			seen[task] = true
		}
		if raw, exists := fields["max_cost"]; exists {
			if summaryNumber(raw, &cost) != nil || cost < 0 || math.IsNaN(cost) || math.IsInf(cost, 0) {
				failure(w, 400, "invalid_request")
				return
			}
		}
	}
	timeout := 10 * time.Second
	if generate {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	if ctx.Err() != nil {
		failure(w, 422, "admission_denied")
		return
	}
	var output any
	if generate {
		a, err := callSkillGeneration(func() (skills.GenerationAttempt, error) {
			return h.services.GenerateSkillDraft(ctx, id, model, name, tasks, cost)
		})
		if err != nil || ctx.Err() != nil {
			failure(w, 422, "admission_denied")
			return
		}
		// Model is the provider name, not the requested configured alias. The
		// service owns alias resolution and configured scope admission.
		if a.Validate() != nil || a.Status != "drafted" || a.ID != id || a.Key.Name != name {
			failure(w, 500, "invalid_service_result")
			return
		}
		output = a
	} else {
		v, err := callSkillGeneration(func() (skills.Version, error) { return h.services.PublishSkillGeneration(ctx, id) })
		if err != nil || ctx.Err() != nil {
			failure(w, 422, "admission_denied")
			return
		}
		if v.Validate() != nil {
			failure(w, 500, "invalid_service_result")
			return
		}
		output = v
	}
	encoded, err := json.Marshal(output)
	if err != nil || len(encoded) > 512<<10 {
		failure(w, 500, "invalid_service_result")
		return
	}
	if ctx.Err() != nil {
		failure(w, 422, "admission_denied")
		return
	}
	writeJSON(w, 200, output)
}

// Host callbacks remain cooperative trusted code; a panic never leaks its
// payload or a partially returned proposal into the HTTP response.
func callSkillGeneration[T any](call func() (T, error)) (result T, err error) {
	defer func() {
		if recover() != nil {
			var zero T
			result = zero
			err = errors.New("skill generation failed")
		}
	}()
	return call()
}
