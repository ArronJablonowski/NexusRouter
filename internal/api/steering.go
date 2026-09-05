package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func (h *Handler) serveSteering(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/tasks/"), "/")
	post := r.Method == http.MethodPost && len(parts) == 2
	get := r.Method == http.MethodGet && len(parts) == 3
	list := r.Method == http.MethodGet && len(parts) == 2
	if (!post && !get && !list) || !replayTaskID(parts[0]) || parts[1] != "steering" || (get && !replayTaskID(parts[2])) {
		failure(w, 404, "not_found")
		return
	}
	if post && h.services.Steer == nil || get && h.services.Steering == nil || list && h.services.SteeringList == nil {
		failure(w, 503, "steering_unavailable")
		return
	}
	select {
	case h.steeringSlots <- struct{}{}:
		defer func() { <-h.steeringSlots }()
	default:
		failure(w, 503, "steering_capacity")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if list {
		if r.ContentLength != 0 || len(r.TransferEncoding) > 0 {
			failure(w, 400, "invalid_request")
			return
		}
		h.serveSteeringList(ctx, w, parts[0])
		return
	}
	var message runtime.SteeringMessage
	var err error
	if post {
		media, _, parseErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if parseErr != nil || media != "application/json" {
			failure(w, 415, "json_required")
			return
		}
		body, readErr := io.ReadAll(http.MaxBytesReader(w, r.Body, 512<<10))
		if readErr != nil {
			failure(w, 413, "invalid_request")
			return
		}
		if !utf8.Valid(body) {
			failure(w, 400, "invalid_request")
			return
		}
		fields, parseErr := chatObject(body, "idempotency_key", "text")
		var key, text string
		if parseErr != nil || len(fields) != 2 || json.Unmarshal(fields["idempotency_key"], &key) != nil || json.Unmarshal(fields["text"], &text) != nil || strings.TrimSpace(key) == "" || len(key) > 128 || !runtime.ValidSteeringText(text) {
			failure(w, 400, "invalid_request")
			return
		}
		message, err = h.services.Steer(ctx, parts[0], key, text)
	} else {
		if r.ContentLength != 0 || len(r.TransferEncoding) > 0 {
			failure(w, 400, "invalid_request")
			return
		}
		message, err = h.services.Steering(ctx, parts[0], parts[2])
	}
	if err != nil {
		status, code := 500, "steering_unavailable"
		switch {
		case errors.Is(err, sql.ErrNoRows):
			status, code = 404, "steering_not_found"
		case errors.Is(err, runtime.ErrSteeringClosed), errors.Is(err, telemetry.ErrConflict):
			status, code = 409, "steering_conflict"
		case errors.Is(err, runtime.ErrSteeringLimit):
			status, code = 429, "steering_limit"
		case errors.Is(err, app.ErrAdmission):
			status, code = 400, "invalid_request"
		}
		failure(w, status, code)
		return
	}
	if message.Validate() != nil || message.TaskID != parts[0] || (get && message.ID != parts[2]) {
		failure(w, 500, "invalid_steering_status")
		return
	}
	status := 200
	if post && message.State == "pending" {
		status = 202
	}
	// Never echo submitted guidance or idempotency keys in control responses.
	writeJSON(w, status, message.Receipt())
}

func (h *Handler) serveSteeringList(ctx context.Context, w http.ResponseWriter, task string) {
	messages, err := h.services.SteeringList(ctx, task)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			failure(w, 404, "steering_not_found")
		} else {
			failure(w, 500, "steering_unavailable")
		}
		return
	}
	if len(messages) > runtime.MaxSteeringMessages {
		failure(w, 500, "invalid_steering_status")
		return
	}
	receipts := make([]runtime.SteeringReceipt, 0, len(messages))
	seen := map[string]bool{}
	for _, m := range messages {
		if m.Validate() != nil || m.TaskID != task || seen[m.ID] {
			failure(w, 500, "invalid_steering_status")
			return
		}
		seen[m.ID] = true
		receipts = append(receipts, m.Receipt())
	}
	writeJSON(w, 200, struct {
		Version  int                       `json:"version"`
		TaskID   string                    `json:"task_id"`
		Messages []runtime.SteeringReceipt `json:"messages"`
	}{1, task, receipts})
}
