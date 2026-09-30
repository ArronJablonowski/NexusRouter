package api

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"time"

	"github.com/ArronJablonowski/NexusRouter/skills"
)

func (h *Handler) serveSkillComparisonSelection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		failure(w, 405, "method_not_allowed")
		return
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery || r.ContentLength < 0 || len(r.TransferEncoding) != 0 {
		failure(w, 400, "invalid_request")
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		failure(w, 415, "json_required")
		return
	}
	if r.Body == nil || r.ContentLength > 64<<10 {
		failure(w, 413, "invalid_request")
		return
	}
	if h.services.SelectSkillComparison == nil {
		failure(w, 503, "skill_comparison_unavailable")
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
		failure(w, 503, "skill_comparison_unavailable")
		return
	}
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(5 * time.Second))
	defer controller.SetReadDeadline(time.Time{})
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	input, decodeErr := decodeSkillComparisonSelection(body)
	if err != nil || decodeErr != nil {
		failure(w, 400, "invalid_request")
		return
	}
	if ctx.Err() != nil {
		failure(w, 503, "skill_comparison_unavailable")
		return
	}
	report, err := callSkillGeneration(func() (skills.ComparisonSelectionReport, error) { return h.services.SelectSkillComparison(ctx, input) })
	if err != nil || ctx.Err() != nil {
		failure(w, 503, "skill_comparison_unavailable")
		return
	}
	p := report.Policy.Comparison
	if report.Validate() != nil || report.ConfiguredModelID != input.ModelID || p.Key.Name != input.Name || p.BaselineVersion != input.BaselineVersion || p.CandidateVersion != input.CandidateVersion || p.Execution.Domain != input.Domain || p.Execution.Profile != input.Profile || p.Source != input.Source || p.MinSamples != input.MinSamples || p.MinDrop != input.MinDrop || report.Policy.Privacy != input.Privacy || report.Policy.TasksPerVersion != input.TasksPerVersion {
		failure(w, 500, "invalid_skill_comparison")
		return
	}
	writeJSON(w, 200, report)
}

func decodeSkillComparisonSelection(body []byte) (skills.ComparisonSelectionRequest, error) {
	var input skills.ComparisonSelectionRequest
	if len(body) > 64<<10 || !memoryJSONUnicodeValid(body) {
		return input, skills.ErrInvalid
	}
	fields, err := chatObject(body, "version", "model_id", "domain", "profile", "name", "baseline_version", "candidate_version", "source", "min_samples", "min_drop", "privacy", "tasks_per_version")
	if err != nil || len(fields) != 12 {
		return input, skills.ErrInvalid
	}
	for _, raw := range fields {
		if string(raw) == "null" {
			return input, skills.ErrInvalid
		}
	}
	if json.Unmarshal(body, &input) != nil || input.Validate() != nil {
		return skills.ComparisonSelectionRequest{}, skills.ErrInvalid
	}
	return input, nil
}
