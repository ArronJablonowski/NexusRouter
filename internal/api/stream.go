package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
	"github.com/ArronJablonowski/DarwinRouter/submissions"
)

const taskStreamPollInterval = 25 * time.Millisecond

type taskStreamCursor struct {
	id      string
	after   int64
	present bool
}

// serveTaskStream admits work through the durable submission journal and tails
// its committed event order. The request owns observation only; disconnecting
// never cancels or otherwise mutates dispatcher-owned execution.
func (h *Handler) serveTaskStream(w http.ResponseWriter, r *http.Request) {
	if h.services.Submit == nil || h.services.SubmissionStream == nil {
		failure(w, http.StatusServiceUnavailable, "durable_streaming_unavailable")
		return
	}
	key, ok := submissionKey(r.Header)
	if !ok {
		failure(w, http.StatusBadRequest, "invalid_idempotency_key")
		return
	}
	cursor, err := parseTaskStreamCursor(r.Header)
	if err != nil {
		failure(w, http.StatusBadRequest, "invalid_event_cursor")
		return
	}
	if cursor.present && h.services.ResumeSubmission == nil {
		failure(w, http.StatusServiceUnavailable, "durable_streaming_unavailable")
		return
	}
	if !submissionJSONMedia(w, r) {
		return
	}
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		w.Header().Set("Retry-After", "1")
		failure(w, http.StatusServiceUnavailable, "capacity")
		return
	}
	if !taskStreamWriter(w) {
		failure(w, http.StatusInternalServerError, "streaming_unavailable")
		return
	}
	req, err := decodeRequest(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		failure(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if r.Context().Err() != nil {
		return
	}
	var status submissions.Status
	if cursor.present {
		status, err = h.services.ResumeSubmission(r.Context(), key, req)
	} else {
		status, err = h.services.Submit(r.Context(), key, req)
	}
	if err != nil {
		taskStreamAdmissionFailure(w, err, cursor.present)
		return
	}
	if !validSubmissionStatus(status, "") {
		failure(w, http.StatusInternalServerError, "invalid_submission_status")
		return
	}
	if cursor.present && cursor.id != status.ID {
		failure(w, http.StatusConflict, "event_cursor_conflict")
		return
	}
	// Read and validate the first snapshot before committing SSE headers. This
	// keeps foreign, stale, ahead, and corrupt cursors as ordinary HTTP errors.
	page, err := h.services.SubmissionStream(r.Context(), status.ID, cursor.after, 100)
	if err != nil {
		taskStreamCursorFailure(w, err)
		return
	}
	if !validTaskStreamPage(page, status.ID, cursor.after) {
		failure(w, http.StatusInternalServerError, "invalid_stream_page")
		return
	}
	writer := newTaskSSEWriter(w, r, status.ID)
	if writer.start() != nil {
		return
	}
	// After SSE begins, only durable events/results are emitted. A reader or
	// transport failure ends observation without inventing a terminal outcome.
	_ = h.tailTaskSubmission(r.Context(), writer, page)
}

func parseTaskStreamCursor(header http.Header) (taskStreamCursor, error) {
	var values []string
	for key, items := range header {
		if strings.EqualFold(key, "Last-Event-ID") {
			if values != nil || len(items) != 1 {
				return taskStreamCursor{}, sessions.ErrEventCursor
			}
			values = items
		}
	}
	if values == nil {
		return taskStreamCursor{}, nil
	}
	id, number, ok := strings.Cut(values[0], ":")
	after, err := strconv.ParseInt(number, 10, 64)
	if !ok || !replayTaskID(id) || err != nil || after < 0 || strconv.FormatInt(after, 10) != number {
		return taskStreamCursor{}, sessions.ErrEventCursor
	}
	return taskStreamCursor{id: id, after: after, present: true}, nil
}

func taskStreamAdmissionFailure(w http.ResponseWriter, err error, resuming bool) {
	switch {
	case errors.Is(err, submissions.ErrConflict):
		failure(w, http.StatusConflict, "task_conflict")
	case errors.Is(err, submissions.ErrCapacity):
		w.Header().Set("Retry-After", "1")
		failure(w, http.StatusServiceUnavailable, "submission_capacity")
	case errors.Is(err, app.ErrAdmission):
		failure(w, http.StatusUnprocessableEntity, "admission_denied")
	case resuming && errors.Is(err, sql.ErrNoRows):
		failure(w, http.StatusConflict, "event_cursor_conflict")
	default:
		failure(w, http.StatusInternalServerError, "task_unavailable")
	}
}

func taskStreamCursorFailure(w http.ResponseWriter, err error) {
	status, code := http.StatusInternalServerError, "events_unavailable"
	switch {
	case errors.Is(err, sessions.ErrEventCursor), errors.Is(err, sql.ErrNoRows):
		status, code = http.StatusConflict, "event_cursor_conflict"
	case errors.Is(err, sessions.ErrEventTooLarge):
		status, code = http.StatusRequestEntityTooLarge, "event_too_large"
	}
	failure(w, status, code)
}

func validTaskStreamPage(page submissions.StreamPage, id string, after int64) bool {
	if page.Version != 1 || page.SubmissionID != id || after < 0 || page.FromSequence != after || page.NextSequence < after || page.EventHeadSequence < 0 || page.ResultSequence < 0 || len(page.Events) > 100 || !validSubmissionStatus(page.Status, id) || page.EventHeadSequence < int64(len(page.Status.TaskIDs)) {
		return false
	}
	terminal := terminalSubmission(page.Status.State)
	if terminal != (page.ResultSequence == page.EventHeadSequence+1) || !terminal && page.ResultSequence != 0 {
		return false
	}
	max := page.EventHeadSequence
	if terminal {
		max = page.ResultSequence
	}
	if page.NextSequence > max || page.HasMoreEvents != (page.NextSequence < page.EventHeadSequence) {
		return false
	}
	if after > page.EventHeadSequence && (!terminal || after != page.ResultSequence || len(page.Events) != 0 || page.NextSequence != after) {
		return false
	}
	knownTasks := make(map[string]bool, len(page.Status.TaskIDs))
	for _, task := range page.Status.TaskIDs {
		knownTasks[task] = true
	}
	knownEvents := make(map[string]bool, len(page.Events))
	for i, item := range page.Events {
		if item.Sequence != after+int64(i)+1 || item.Event.Validate() != nil || !knownTasks[item.Event.TaskID] || knownEvents[item.Event.ID] || item.Event.Kind == runtime.TaskStarted && item.Event.Data.SubmissionID != id {
			return false
		}
		knownEvents[item.Event.ID] = true
	}
	return page.NextSequence-after == int64(len(page.Events))
}

func (h *Handler) tailTaskSubmission(ctx context.Context, writer *taskSSEWriter, page submissions.StreamPage) error {
	ticker := time.NewTicker(taskStreamPollInterval)
	defer ticker.Stop()
	for {
		for _, item := range page.Events {
			if err := writer.event(item.Sequence, item.Event); err != nil {
				return err
			}
		}
		if page.HasMoreEvents {
			next, err := h.services.SubmissionStream(ctx, page.SubmissionID, page.NextSequence, 100)
			if err != nil {
				return err
			}
			if !validTaskStreamPage(next, page.SubmissionID, page.NextSequence) {
				return submissions.ErrInvalid
			}
			page = next
			continue
		}
		if page.ResultSequence > 0 {
			if page.FromSequence >= page.ResultSequence {
				return nil
			}
			return writer.result(page.Status, taskStreamResultCode(page.Status), page.SubmissionID+":"+strconv.FormatInt(page.ResultSequence, 10))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		next, err := h.services.SubmissionStream(ctx, page.SubmissionID, page.NextSequence, 100)
		if err != nil {
			return err
		}
		if !validTaskStreamPage(next, page.SubmissionID, page.NextSequence) {
			return submissions.ErrInvalid
		}
		page = next
	}
}

func taskStreamResultCode(status submissions.Status) string {
	if status.State == "succeeded" {
		return ""
	}
	if status.State == "canceled" {
		return "canceled"
	}
	if status.ErrorCode == "admission_denied" {
		return "admission_denied"
	}
	return "task_failed"
}

type taskSSEWriter struct {
	w            http.ResponseWriter
	r            *http.Request
	controller   *http.ResponseController
	submissionID string
	broken       bool
}

func newTaskSSEWriter(w http.ResponseWriter, r *http.Request, submissionID string) *taskSSEWriter {
	return &taskSSEWriter{w: w, r: r, controller: http.NewResponseController(w), submissionID: submissionID}
}

func (s *taskSSEWriter) start() error {
	s.w.Header().Set("Content-Type", "text/event-stream")
	s.w.Header().Set("Cache-Control", "no-store")
	s.w.Header().Set("X-Accel-Buffering", "no")
	s.w.WriteHeader(http.StatusOK)
	return s.flush()
}

func (s *taskSSEWriter) event(sequence int64, event runtime.Event) error {
	if event.Validate() != nil {
		return app.ErrEventDelivery
	}
	body, err := json.Marshal(event)
	if err != nil {
		return app.ErrEventDelivery
	}
	return s.write(string(event.Kind), strconv.FormatInt(sequence, 10), body)
}

func (s *taskSSEWriter) result(status submissions.Status, code, id string) error {
	body := map[string]any{"submission_id": status.ID, "task_ids": status.TaskIDs}
	if status.Result != nil {
		body["task_id"] = status.Result.TaskID
		body["previous_task_ids"] = status.Result.PreviousTaskIDs
		body["route_estimated_cost"] = status.Result.RouteEstimatedCost
		body["audit_id"] = status.Result.AuditID
		body["audit_status"] = status.Result.AuditStatus
		if code == "" {
			body["text"] = status.Result.Text
			body["turns"] = status.Result.Turns
			body["finish_reason"] = status.Result.FinishReason
			body["usage"] = status.Result.Usage
		}
	}
	if code != "" {
		body["error"] = code
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return app.ErrEventDelivery
	}
	return s.write("result", id, encoded)
}

func (s *taskSSEWriter) flush() (err error) {
	defer func() {
		if recover() != nil {
			err = app.ErrEventDelivery
		}
		if err != nil {
			s.broken = true
		}
	}()
	if s.broken || s.r.Context().Err() != nil {
		return app.ErrEventDelivery
	}
	if err = s.controller.SetWriteDeadline(time.Now().Add(15 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	if err = s.controller.Flush(); err != nil {
		return err
	}
	err = s.controller.SetWriteDeadline(time.Time{})
	if errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	return err
}

func (s *taskSSEWriter) write(kind, id string, body []byte) (err error) {
	if s.broken || s.r.Context().Err() != nil || kind == "" {
		return app.ErrEventDelivery
	}
	defer func() {
		if recover() != nil {
			err = app.ErrEventDelivery
		}
		if err != nil {
			s.broken = true
		}
	}()
	if err = s.controller.SetWriteDeadline(time.Now().Add(15 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	frame := "event: " + kind + "\n"
	if id != "" {
		frame += "id: " + sIDPrefix(s, id) + "\n"
	}
	frame += "data: " + string(body) + "\n\n"
	n, err := io.WriteString(s.w, frame)
	if err != nil {
		return err
	}
	if n != len(frame) {
		return io.ErrShortWrite
	}
	return s.flush()
}

func sIDPrefix(s *taskSSEWriter, id string) string {
	// result already supplies its complete durable cursor; event IDs receive the
	// submission prefix from the request-scoped writer state below.
	if strings.Contains(id, ":") {
		return id
	}
	return s.submissionID + ":" + id
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
