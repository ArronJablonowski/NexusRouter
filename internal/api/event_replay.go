package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"darwinrouter/sessions"
)

func replayTaskID(id string) bool {
	return id != "" && len(id) <= 128 && utf8.ValidString(id) && !strings.ContainsAny(id, "/:") && !strings.ContainsFunc(id, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) })
}

func replayCursor(header http.Header, task string) (int64, error) {
	var values []string
	for key, items := range header {
		if strings.EqualFold(key, "Last-Event-ID") {
			if len(items) != 1 || values != nil {
				return 0, sessions.ErrEventCursor
			}
			values = items
		}
	}
	if values == nil {
		return 0, nil
	}
	value := values[0]
	if !utf8.ValidString(value) || !strings.HasPrefix(value, task+":") {
		return 0, sessions.ErrEventCursor
	}
	number := strings.TrimPrefix(value, task+":")
	after, err := strconv.ParseInt(number, 10, 64)
	if err != nil || after < 0 || strconv.FormatInt(after, 10) != number {
		return 0, sessions.ErrEventCursor
	}
	return after, nil
}

// serveEventReplay emits one validated durable snapshot page. Reading or
// disconnecting never starts, resumes, or cancels the underlying task.
func (h *Handler) serveEventReplay(w http.ResponseWriter, r *http.Request) {
	task := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/tasks/"), "/events")
	if !replayTaskID(task) {
		failure(w, 400, "invalid_task_id")
		return
	}
	if h.services.Events == nil {
		failure(w, 503, "events_unavailable")
		return
	}
	after, err := replayCursor(r.Header, task)
	if err != nil {
		failure(w, 400, "invalid_event_cursor")
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
	page, err := h.services.Events(r.Context(), task, after, 100)
	if r.Context().Err() != nil {
		return
	}
	if err != nil {
		status, code := 500, "events_unavailable"
		switch {
		case errors.Is(err, sql.ErrNoRows):
			status, code = 404, "task_unavailable"
		case errors.Is(err, sessions.ErrEventCursor):
			status, code = 409, "event_cursor_conflict"
		case errors.Is(err, sessions.ErrEventTooLarge):
			status, code = 413, "event_too_large"
		}
		failure(w, status, code)
		return
	}
	pageErr := page.Validate()
	if errors.Is(pageErr, sessions.ErrEventTooLarge) {
		failure(w, 413, "event_too_large")
		return
	}
	if pageErr != nil || page.TaskID != task || page.FromSequence != after || len(page.Events) > 100 {
		failure(w, 500, "invalid_event_page")
		return
	}
	bodies := make([][]byte, len(page.Events))
	total := 0
	for i, e := range page.Events {
		body, err := json.Marshal(e)
		if err != nil {
			failure(w, 500, "invalid_event_page")
			return
		}
		total += len(body)
		if total > 8<<20 {
			failure(w, 413, "event_too_large")
			return
		}
		bodies[i] = body
	}
	// Marshal explicit metadata so the checkpoint cannot duplicate event data
	// or accidentally acquire a resume ID or imply a reconstructed run result.
	checkpoint, err := json.Marshal(struct {
		Version      int    `json:"version"`
		TaskID       string `json:"task_id"`
		SessionID    string `json:"session_id"`
		State        string `json:"state"`
		FromSequence int64  `json:"from_sequence"`
		NextSequence int64  `json:"next_sequence"`
		HeadSequence int64  `json:"head_sequence"`
		HasMore      bool   `json:"has_more"`
	}{page.Version, page.TaskID, page.SessionID, page.State, page.FromSequence, page.NextSequence, page.HeadSequence, page.HasMore})
	if err != nil {
		failure(w, 500, "invalid_event_page")
		return
	}
	// Every failure from this point leaves only SSE bytes, never a JSON error
	// appended to a partially delivered frame. No writes are retried.
	defer func() { _ = recover() }()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	controller := http.NewResponseController(w)
	flush := func() error {
		if err := controller.Flush(); err != nil {
			return err
		}
		err := controller.SetWriteDeadline(time.Time{})
		if errors.Is(err, http.ErrNotSupported) {
			return nil
		}
		return err
	}
	deadline := func() error {
		err := controller.SetWriteDeadline(time.Now().Add(15 * time.Second))
		if errors.Is(err, http.ErrNotSupported) {
			return nil
		}
		return err
	}
	if r.Context().Err() != nil {
		return
	}
	if deadline() != nil || flush() != nil {
		return
	}
	write := func(kind, id string, body []byte) error {
		if r.Context().Err() != nil {
			return r.Context().Err()
		}
		if err := deadline(); err != nil {
			return err
		}
		frame := "event: " + kind + "\n"
		if id != "" {
			frame += "id: " + id + "\n"
		}
		frame += "data: " + string(body) + "\n\n"
		n, err := io.WriteString(w, frame)
		if err != nil {
			return err
		}
		if n != len(frame) {
			return io.ErrShortWrite
		}
		return flush()
	}
	for i, e := range page.Events {
		if write(string(e.Kind), task+":"+strconv.FormatInt(e.Sequence, 10), bodies[i]) != nil {
			return
		}
	}
	_ = write("checkpoint", "", checkpoint)
}
