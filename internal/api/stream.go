package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

// serveTaskStream delivers durable lifecycle events, not raw provider tokens.
// A new POST creates a task; Last-Event-ID cannot authorize re-execution.
func (h *Handler) serveTaskStream(w http.ResponseWriter, r *http.Request) {
	if h.services.RunStream == nil {
		failure(w, 503, "streaming_unavailable")
		return
	}
	for key := range r.Header {
		if strings.EqualFold(key, "Last-Event-ID") {
			failure(w, 400, "resume_not_supported")
			return
		}
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		failure(w, 415, "json_required")
		return
	}
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		w.Header().Set("Retry-After", "1")
		failure(w, 503, "capacity")
		return
	}
	if !taskStreamWriter(w) {
		failure(w, 500, "streaming_unavailable")
		return
	}
	req, err := decodeRequest(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		failure(w, 400, "invalid_request")
		return
	}
	if r.Context().Err() != nil {
		return
	}
	controller := http.NewResponseController(w)
	started, broken := false, false
	write := func(kind, id string, body []byte) (err error) {
		if broken {
			return app.ErrEventDelivery
		}
		defer func() {
			if recover() != nil {
				err = app.ErrEventDelivery
			}
			if err != nil {
				broken = true
			}
		}()
		if r.Context().Err() != nil {
			return r.Context().Err()
		}
		if err = controller.SetWriteDeadline(time.Now().Add(15 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return err
		}
		frame := "event: " + kind + "\n"
		if id != "" {
			frame += "id: " + id + "\n"
		}
		frame += "data: " + string(body) + "\n\n"
		n := 0
		if n, err = io.WriteString(w, frame); err != nil {
			return err
		}
		if n != len(frame) {
			return io.ErrShortWrite
		}
		if err = controller.Flush(); err != nil {
			return err
		}
		err = controller.SetWriteDeadline(time.Time{})
		if errors.Is(err, http.ErrNotSupported) {
			return nil
		}
		return err
	}
	finish := func(result app.Result, code string) {
		body := map[string]any{"task_id": result.TaskID, "text": result.Text, "turns": result.Turns, "finish_reason": result.FinishReason, "usage": result.Usage, "previous_task_ids": result.PreviousTaskIDs, "audit_id": result.AuditID, "audit_status": result.AuditStatus}
		if code != "" {
			body = map[string]any{"task_id": result.TaskID, "previous_task_ids": result.PreviousTaskIDs, "audit_id": result.AuditID, "error": code}
		}
		encoded, err := json.Marshal(body)
		if err != nil {
			encoded = []byte(`{"error":"internal_error"}`)
		}
		_ = write("result", "", encoded)
	}
	defer func() {
		if recover() != nil {
			if started {
				finish(app.Result{}, "internal_error")
			} else {
				failure(w, 500, "internal_error")
			}
		}
	}()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	started = true
	w.WriteHeader(http.StatusOK)
	// Make the streaming response observable even when the service is still
	// performing admission or preparing its first durable task event.
	flushHeaders := func() (err error) {
		defer func() {
			if recover() != nil {
				err = app.ErrEventDelivery
			}
		}()
		if err = controller.SetWriteDeadline(time.Now().Add(15 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return err
		}
		if err = controller.Flush(); err != nil {
			return err
		}
		err = controller.SetWriteDeadline(time.Time{})
		if errors.Is(err, http.ErrNotSupported) {
			return nil
		}
		return err
	}
	if err := flushHeaders(); err != nil {
		return
	}
	result, runErr := h.services.RunStream(r.Context(), req, func(e runtime.Event) error {
		if e.Validate() != nil || len(e.TaskID) > 128 || !utf8.ValidString(e.TaskID) || strings.ContainsAny(e.TaskID, "\r\n\x00:") || strings.ContainsFunc(e.TaskID, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
			return app.ErrEventDelivery
		}
		body, err := json.Marshal(e)
		if err != nil {
			return app.ErrEventDelivery
		}
		return write(string(e.Kind), e.TaskID+":"+strconv.FormatInt(e.Sequence, 10), body)
	})
	code := ""
	if runErr != nil {
		result.Text = ""
		code = "task_failed"
		switch {
		case errors.Is(runErr, app.ErrEventDelivery):
			code = "event_delivery_failed"
		case errors.Is(runErr, app.ErrAdmission):
			code = "admission_denied"
		case errors.Is(runErr, context.Canceled):
			code = "canceled"
		case errors.Is(runErr, context.DeadlineExceeded):
			code = "deadline_exceeded"
		}
	}
	finish(result, code)
}

// ResponseController unwraps middleware writers; mirror its flush capability
// discovery without flushing or starting the response during admission.
func taskStreamWriter(w http.ResponseWriter) bool {
	for i := 0; i < 16; i++ {
		switch writer := w.(type) {
		case interface{ FlushError() error }:
			return true
		case http.Flusher:
			return true
		case interface{ Unwrap() http.ResponseWriter }:
			w = writer.Unwrap()
		default:
			return false
		}
	}
	return false
}
