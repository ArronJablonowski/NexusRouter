package api

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
)

func (h *Handler) serveDeprecation(w http.ResponseWriter, r *http.Request) {
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
	if r.Body == nil || r.ContentLength > 4096 {
		failure(w, 413, "invalid_request")
		return
	}
	if h.services.ModelDeprecation == nil {
		failure(w, 503, "deprecation_unavailable")
		return
	}
	select {
	case h.deprecationSlots <- struct{}{}:
		defer func() { <-h.deprecationSlots }()
	default:
		w.Header().Set("Retry-After", "1")
		failure(w, 503, "capacity")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if ctx.Err() != nil {
		failure(w, 503, "deprecation_unavailable")
		return
	}
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(5 * time.Second))
	defer http.NewResponseController(w).SetReadDeadline(time.Time{})
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	request, decodeErr := decodeDeprecation(body)
	if err != nil || decodeErr != nil {
		failure(w, 400, "invalid_request")
		return
	}
	if ctx.Err() != nil {
		failure(w, 503, "deprecation_unavailable")
		return
	}
	report, ok := h.callDeprecation(ctx, request)
	if !ok || ctx.Err() != nil || report.Validate() != nil || report.Policy != request.Policy || report.ConfiguredModelID == "" || report.Key.Model == "" {
		failure(w, 503, "deprecation_unavailable")
		return
	}
	writeJSON(w, 200, report)
}

func decodeDeprecation(body []byte) (evaluation.DeprecationRequest, error) {
	var request evaluation.DeprecationRequest
	if !utf8.Valid(body) {
		return request, evaluation.ErrDeprecation
	}
	fields, err := chatObject(body, "version", "model_id", "domain", "profile", "policy")
	if err != nil || len(fields) != 5 {
		return request, evaluation.ErrDeprecation
	}
	policy, err := chatObject(fields["policy"], "window", "min_samples", "failure_threshold")
	if err != nil || len(policy) != 3 {
		return request, evaluation.ErrDeprecation
	}
	// Strict objects prevent aliases/duplicates; typed decoding rejects strings,
	// fractions and overflow for integer policy fields. Validate pins semantics.
	for _, field := range fields {
		if string(field) == "null" {
			return request, evaluation.ErrDeprecation
		}
	}
	for _, field := range policy {
		if string(field) == "null" {
			return request, evaluation.ErrDeprecation
		}
	}
	if json.Unmarshal(body, &request) != nil || request.Validate() != nil {
		return evaluation.DeprecationRequest{}, evaluation.ErrDeprecation
	}
	return request, nil
}

func (h *Handler) callDeprecation(ctx context.Context, request evaluation.DeprecationRequest) (report evaluation.DeprecationReport, ok bool) {
	defer func() {
		if recover() != nil {
			report = evaluation.DeprecationReport{}
			ok = false
		}
	}()
	var err error
	report, err = h.services.ModelDeprecation(ctx, request.ModelID, request.Domain, request.Profile, request.Policy)
	return report, err == nil
}
