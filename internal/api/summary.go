package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"mime"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

func (h *Handler) serveSummaries(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/summaries":
		h.serveSummaryDraft(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/summaries/query":
		h.serveSummaryQuery(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/summaries/reviews":
		h.serveSummaryReview(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/summaries/"):
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/summaries/"), "/")
		if len(parts) > 2 || !summaryID(parts[0]) || (len(parts) == 2 && parts[1] != "reviews") {
			failure(w, 400, "invalid_summary_id")
			return
		}
		if (len(parts) == 1 && h.services.SummaryAttempt == nil) || (len(parts) == 2 && h.services.SummaryReviews == nil) {
			failure(w, 503, "summary_unavailable")
			return
		}
		release, ok := h.summaryPermit(w, r)
		if !ok {
			return
		}
		defer release()
		var result any
		var err error
		if len(parts) == 1 {
			result, err = h.services.SummaryAttempt(r.Context(), parts[0])
		} else {
			result, err = h.services.SummaryReviews(r.Context(), parts[0])
		}
		if err != nil {
			failure(w, 404, "summary_unavailable")
			return
		}
		writeJSON(w, 200, result)
	default:
		failure(w, 404, "not_found")
	}
}

func (h *Handler) serveSummaryDraft(w http.ResponseWriter, r *http.Request) {
	if h.services.Summarize == nil {
		failure(w, 503, "summary_unavailable")
		return
	}
	release, ok := h.summaryPermit(w, r)
	if !ok {
		return
	}
	defer release()
	fields, ok := summaryBody(w, r, "task_id", "model_id", "keep", "max_cost")
	if !ok {
		return
	}
	var task, model string
	var keep int
	var cost float64
	if chatString(fields["task_id"], &task) != nil || !summaryID(task) || chatString(fields["model_id"], &model) != nil || !summaryID(model) || summaryNumber(fields["keep"], &keep) != nil || keep < 1 || keep > 100000 || summaryNumber(fields["max_cost"], &cost) != nil || cost < 0 || math.IsNaN(cost) || math.IsInf(cost, 0) {
		failure(w, 400, "invalid_request")
		return
	}
	a, err := h.services.Summarize(r.Context(), task, model, keep, cost)
	if err != nil {
		status, code := summaryError(err)
		id := ""
		if summaryID(a.ID) {
			id = a.ID
		}
		writeJSON(w, status, map[string]string{"error": code, "summary_attempt_id": id})
		return
	}
	writeJSON(w, 201, a)
}

func (h *Handler) serveSummaryQuery(w http.ResponseWriter, r *http.Request) {
	if h.services.SummaryAttempts == nil {
		failure(w, 503, "summary_unavailable")
		return
	}
	release, ok := h.summaryPermit(w, r)
	if !ok {
		return
	}
	defer release()
	fields, ok := summaryBody(w, r, "task_id", "after", "limit")
	if !ok {
		return
	}
	var task, after string
	limit := 100
	for key, target := range map[string]*string{"task_id": &task, "after": &after} {
		if raw, present := fields[key]; present && (chatString(raw, target) != nil || (*target != "" && !summaryID(*target))) {
			failure(w, 400, "invalid_request")
			return
		}
	}
	if raw, present := fields["limit"]; present && (summaryNumber(raw, &limit) != nil || limit < 1 || limit > 100) {
		failure(w, 400, "invalid_request")
		return
	}
	result, err := h.services.SummaryAttempts(r.Context(), task, after, limit)
	if err != nil {
		status, code := summaryError(err)
		failure(w, status, code)
		return
	}
	writeJSON(w, 200, result)
}

func (h *Handler) serveSummaryReview(w http.ResponseWriter, r *http.Request) {
	if h.services.ReviewSummary == nil {
		failure(w, 503, "summary_unavailable")
		return
	}
	release, ok := h.summaryPermit(w, r)
	if !ok {
		return
	}
	defer release()
	fields, ok := summaryBody(w, r, "attempt_id", "expected_id", "decision", "note")
	if !ok {
		return
	}
	var attempt, expected, decision, note string
	if chatString(fields["attempt_id"], &attempt) != nil || !summaryID(attempt) || chatString(fields["decision"], &decision) != nil || (decision != "approved" && decision != "rejected") || chatString(fields["note"], &note) != nil || strings.TrimSpace(note) == "" || len(note) > 4096 || !utf8.ValidString(note) {
		failure(w, 400, "invalid_request")
		return
	}
	if raw, present := fields["expected_id"]; present && (chatString(raw, &expected) != nil || (expected != "" && !summaryID(expected))) {
		failure(w, 400, "invalid_request")
		return
	}
	result, err := h.services.ReviewSummary(r.Context(), attempt, expected, decision, note)
	if err != nil {
		status, code := summaryError(err)
		failure(w, status, code)
		return
	}
	writeJSON(w, 201, result)
}

// All summary reads and writes share task capacity. Reserve before body reads.
func (h *Handler) summaryPermit(w http.ResponseWriter, r *http.Request) (func(), bool) {
	if r.Method == http.MethodPost {
		media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || media != "application/json" {
			failure(w, 415, "json_required")
			return nil, false
		}
	}
	select {
	case h.slots <- struct{}{}:
		return func() { <-h.slots }, true
	default:
		w.Header().Set("Retry-After", "1")
		failure(w, 503, "capacity")
		return nil, false
	}
}

func summaryBody(w http.ResponseWriter, r *http.Request, keys ...string) (map[string]json.RawMessage, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<10))
	if err != nil {
		status := 400
		var limit *http.MaxBytesError
		if errors.As(err, &limit) {
			status = 413
		}
		failure(w, status, "invalid_request")
		return nil, false
	}
	fields, err := chatObject(body, keys...)
	if err != nil {
		failure(w, 400, "invalid_request")
		return nil, false
	}
	return fields, true
}

func summaryID(id string) bool {
	return id != "" && len(id) <= 128 && utf8.ValidString(id) && !strings.Contains(id, "/") && !strings.ContainsFunc(id, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) })
}

func summaryNumber(raw json.RawMessage, target any) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || (raw[0] != '-' && (raw[0] < '0' || raw[0] > '9')) {
		return errors.New("invalid number")
	}
	return json.Unmarshal(raw, target)
}
func summaryError(err error) (int, string) {
	if errors.Is(err, app.ErrAdmission) || errors.Is(err, sessions.ErrHistory) {
		return 422, "admission_denied"
	}
	if errors.Is(err, telemetry.ErrConflict) {
		return 409, "summary_conflict"
	}
	return 500, "summary_failed"
}
