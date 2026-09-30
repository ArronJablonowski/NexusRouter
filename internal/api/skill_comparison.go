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

func (h *Handler) serveSkillComparison(w http.ResponseWriter, r *http.Request) {
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
	if h.services.CompareSkillOutcomes == nil {
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
	request, decodeErr := decodeSkillComparison(body)
	if err != nil || decodeErr != nil {
		failure(w, 400, "invalid_request")
		return
	}
	if ctx.Err() != nil {
		failure(w, 503, "skill_comparison_unavailable")
		return
	}
	report, err := callSkillGeneration(func() (skills.ComparisonReport, error) {
		return h.services.CompareSkillOutcomes(ctx, request)
	})
	if err != nil || ctx.Err() != nil {
		failure(w, 503, "skill_comparison_unavailable")
		return
	}
	p := report.Policy
	if report.Validate() != nil || report.ConfiguredModelID != request.ModelID || report.Sampled != len(request.Tasks) || p.Key.Name != request.Name || p.BaselineVersion != request.BaselineVersion || p.CandidateVersion != request.CandidateVersion || p.Execution.Domain != request.Domain || p.Execution.Profile != request.Profile || p.Source != request.Source || p.MinSamples != request.MinSamples || p.MinDrop != request.MinDrop {
		failure(w, 500, "invalid_skill_comparison")
		return
	}
	writeJSON(w, 200, report)
}

func decodeSkillComparison(body []byte) (skills.ComparisonRequest, error) {
	var request skills.ComparisonRequest
	if len(body) > 64<<10 || !memoryJSONUnicodeValid(body) {
		return request, skills.ErrInvalid
	}
	fields, err := chatObject(body, "version", "model_id", "domain", "profile", "name", "baseline_version", "candidate_version", "source", "min_samples", "min_drop", "tasks")
	if err != nil || len(fields) != 11 {
		return request, skills.ErrInvalid
	}
	for _, raw := range fields {
		if string(raw) == "null" {
			return request, skills.ErrInvalid
		}
	}
	if json.Unmarshal(body, &request) != nil || request.Validate() != nil {
		return skills.ComparisonRequest{}, skills.ErrInvalid
	}
	return request, nil
}
