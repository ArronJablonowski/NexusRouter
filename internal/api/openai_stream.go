package api

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
)

// serveChatStream emits provisional redacted text, never tool arguments or raw
// journal deltas. Only successful service completion authorizes finish/DONE.
// An error after headers is an OpenAI-shaped error frame with no success marker.
func (h *Handler) serveChatStream(w http.ResponseWriter, r *http.Request, req app.Request) {
	if h.services.RunTextStream == nil {
		chatFailure(w, 503, "server_error", "streaming_unavailable")
		return
	}
	if !taskStreamWriter(w) {
		chatFailure(w, 500, "server_error", "streaming_unavailable")
		return
	}
	controller := http.NewResponseController(w)
	broken := false
	write := func(data string) (err error) {
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
		if err = r.Context().Err(); err != nil {
			return err
		}
		if err = controller.SetWriteDeadline(time.Now().Add(15 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return err
		}
		frame := "data: " + data + "\n\n"
		n, err := io.WriteString(w, frame)
		if err != nil {
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
	fail := func(code string) {
		kind := "server_error"
		if code == "admission_denied" {
			kind = "invalid_request_error"
		}
		body, _ := json.Marshal(map[string]any{"error": map[string]any{"message": code, "type": kind, "param": nil, "code": code}})
		_ = write(string(body))
	}
	defer func() {
		if recover() != nil {
			fail("internal_error")
		}
	}()
	id, created := "chatcmpl-"+rand.Text(), time.Now().Unix()
	chunk := func(delta map[string]string, finish any) error {
		body, err := json.Marshal(map[string]any{"id": id, "object": "chat.completion.chunk", "created": created, "model": req.ModelID,
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
		if err != nil {
			return err
		}
		return write(string(body))
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-Darwin-Stream-Mode", "live-redacted")
	w.WriteHeader(http.StatusOK)
	if chunk(map[string]string{"role": "assistant"}, nil) != nil {
		return
	}
	used := 0
	var deliveryErr error
	result, runErr := h.services.RunTextStream(r.Context(), req, func(text string) error {
		if deliveryErr != nil {
			return deliveryErr
		}
		if !utf8.ValidString(text) || len(text) > (1<<20)-used {
			deliveryErr = app.ErrEventDelivery
			return deliveryErr
		}
		used += len(text)
		if text != "" {
			deliveryErr = chunk(map[string]string{"content": text}, nil)
		}
		return deliveryErr
	})
	if r.Context().Err() == context.Canceled {
		return
	}
	if runErr != nil || deliveryErr != nil || r.Context().Err() != nil {
		code := "task_failed"
		switch {
		case deliveryErr != nil || errors.Is(runErr, app.ErrEventDelivery):
			code = "event_delivery_failed"
		case errors.Is(runErr, app.ErrAdmission):
			code = "admission_denied"
		case errors.Is(runErr, context.DeadlineExceeded) || r.Context().Err() == context.DeadlineExceeded:
			code = "deadline_exceeded"
		case errors.Is(runErr, context.Canceled):
			code = "canceled"
		}
		fail(code)
		return
	}
	// Result metadata is authoritative; streamed intermediate turns need not
	// equal the final answer, and the final answer must not be appended twice.
	if len(result.Text) > 1<<20 || !utf8.ValidString(result.Text) {
		fail("invalid_upstream_response")
		return
	}
	var finish any
	switch result.FinishReason {
	case "stop", "length", "content_filter", "tool_calls", "function_call":
		finish = result.FinishReason
	}
	if chunk(map[string]string{}, finish) == nil {
		_ = write("[DONE]")
	}
}
