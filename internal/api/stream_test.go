package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
	"github.com/ArronJablonowski/DarwinRouter/submissions"
)

const streamBody = `{"model_id":"m","prompt":"hello"}`

func streamEvent(sequence int64, kind runtime.Kind) runtime.Event {
	return runtime.Event{Version: 1, ID: string(kind), TaskID: "task", SessionID: "session", Sequence: sequence, Time: time.Unix(100, 0).UTC(), Kind: kind, CorrelationID: "task"}
}

type streamFrame struct{ event, id, data string }

func parseStreamFrames(t *testing.T, body string) []streamFrame {
	t.Helper()
	if body == "" {
		return nil
	}
	if !strings.HasSuffix(body, "\n\n") {
		t.Fatalf("unterminated SSE frame: %q", body)
	}
	var frames []streamFrame
	for _, block := range strings.Split(strings.TrimSuffix(body, "\n\n"), "\n\n") {
		frame := streamFrame{}
		for _, line := range strings.Split(block, "\n") {
			key, value, ok := strings.Cut(line, ": ")
			if !ok {
				t.Fatalf("invalid SSE field: %q", line)
			}
			switch key {
			case "event":
				frame.event = value
			case "id":
				frame.id = value
			case "data":
				frame.data = value
			default:
				t.Fatalf("unexpected SSE field: %q", line)
			}
		}
		if frame.event == "" || !json.Valid([]byte(frame.data)) {
			t.Fatalf("invalid SSE frame: %+v", frame)
		}
		frames = append(frames, frame)
	}
	return frames
}

func streamStatus() submissions.Status {
	now := time.Unix(100, 0).UTC()
	return submissions.Status{Version: 1, ID: "submission", State: "succeeded", CreatedAt: now, UpdatedAt: now, ConfigDigest: strings.Repeat("a", 64), TaskIDs: []string{"task"}, Result: &submissions.Result{TaskID: "task", Text: "answer", Turns: 1, FinishReason: "stop"}}
}

func streamPage(status submissions.Status, after int64) (submissions.StreamPage, error) {
	events := []runtime.Event{streamEvent(1, runtime.TaskStarted), streamEvent(2, runtime.TaskCompleted)}
	events[0].Data.SubmissionID = status.ID
	page := submissions.StreamPage{Version: 1, SubmissionID: status.ID, FromSequence: after, NextSequence: after, EventHeadSequence: 2, ResultSequence: 3, Status: status, Events: []submissions.StreamEvent{}}
	if after < 0 || after > 3 {
		return page, sessions.ErrEventCursor
	}
	for i := after; i < 2; i++ {
		page.Events = append(page.Events, submissions.StreamEvent{Sequence: i + 1, Event: events[i]})
		page.NextSequence = i + 1
	}
	page.HasMoreEvents = page.NextSequence < page.EventHeadSequence
	return page, nil
}

func streamServices(submitCalls, resumeCalls *atomic.Int64) Services {
	s := services()
	s.Submit = func(context.Context, string, app.Request) (submissions.Status, error) {
		submitCalls.Add(1)
		return streamStatus(), nil
	}
	s.ResumeSubmission = func(context.Context, string, app.Request) (submissions.Status, error) {
		resumeCalls.Add(1)
		return streamStatus(), nil
	}
	s.SubmissionStream = func(_ context.Context, id string, after int64, _ int) (submissions.StreamPage, error) {
		if id != "submission" {
			return submissions.StreamPage{}, errors.New("wrong submission")
		}
		return streamPage(streamStatus(), after)
	}
	s.RunStream = func(context.Context, app.Request, func(runtime.Event) error) (app.Result, error) {
		panic("request-coupled stream must not run")
	}
	return s
}

func streamRequest(body string) *http.Request {
	r := request(http.MethodPost, "/v1/tasks/stream", body)
	r.Header.Set("Idempotency-Key", "stream-request-key")
	return r
}

func TestTaskStreamStrictPreflightDoesNotAdmit(t *testing.T) {
	for _, condition := range []string{"unauthorized", "origin", "query", "missing-key", "short-key", "duplicate-key", "bad-cursor", "duplicate-cursor", "media", "missing-hook"} {
		t.Run(condition, func(t *testing.T) {
			var submits, resumes atomic.Int64
			s := streamServices(&submits, &resumes)
			if condition == "missing-hook" {
				s.SubmissionStream = nil
			}
			h, err := New(token, 1, s)
			if err != nil {
				t.Fatal(err)
			}
			r := streamRequest(streamBody)
			want := http.StatusBadRequest
			switch condition {
			case "unauthorized":
				r.Header.Del("Authorization")
				want = http.StatusUnauthorized
			case "origin":
				r.Header.Set("Origin", "https://example.com")
				want = http.StatusForbidden
			case "query":
				r.URL.RawQuery = "private=secret"
			case "missing-key":
				r.Header.Del("Idempotency-Key")
			case "short-key":
				r.Header.Set("Idempotency-Key", "short")
			case "duplicate-key":
				r.Header["Idempotency-Key"] = []string{"stream-request-key", "stream-request-key"}
			case "bad-cursor":
				r.Header.Set("Last-Event-ID", "submission:not-a-number")
			case "duplicate-cursor":
				r.Header["Last-Event-ID"] = []string{"submission:1", "submission:1"}
			case "media":
				r.Header.Set("Content-Type", "text/plain")
				want = http.StatusUnsupportedMediaType
			case "missing-hook":
				want = http.StatusServiceUnavailable
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != want || submits.Load() != 0 || resumes.Load() != 0 || !json.Valid(w.Body.Bytes()) || strings.Contains(w.Body.String(), "private") {
				t.Fatalf("status=%d submit=%d resume=%d body=%q", w.Code, submits.Load(), resumes.Load(), w.Body.String())
			}
		})
	}
}

func TestTaskStreamIdempotencyKeyBoundariesAndCharacters(t *testing.T) {
	for name, key := range map[string]string{
		"missing":         "",
		"fifteen":         strings.Repeat("a", 15),
		"one-twenty-nine": strings.Repeat("a", 129),
		"space":           "0123456789abcde ",
		"tab":             "0123456789abcde\t",
		"unit-separator":  "0123456789abcde\x1f",
		"delete":          "0123456789abcde\x7f",
		"non-ascii":       "0123456789abcdeé",
	} {
		t.Run(name, func(t *testing.T) {
			var submits, resumes atomic.Int64
			h, err := New(token, 1, streamServices(&submits, &resumes))
			if err != nil {
				t.Fatal(err)
			}
			r := streamRequest(streamBody)
			if key == "" {
				r.Header.Del("Idempotency-Key")
			} else {
				r.Header.Set("Idempotency-Key", key)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusBadRequest || w.Body.String() != "{\"error\":\"invalid_idempotency_key\"}\n" || submits.Load() != 0 || resumes.Load() != 0 {
				t.Fatalf("status=%d body=%q submit=%d resume=%d", w.Code, w.Body.String(), submits.Load(), resumes.Load())
			}
		})
	}

	for name, mutate := range map[string]func(http.Header){
		"duplicate": func(header http.Header) {
			header["Idempotency-Key"] = []string{"0123456789abcdef", "0123456789abcdef"}
		},
		"case-aliased": func(header http.Header) {
			header["Idempotency-Key"] = []string{"0123456789abcdef"}
			header["idempotency-key"] = []string{"0123456789abcdef"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			var submits, resumes atomic.Int64
			h, _ := New(token, 1, streamServices(&submits, &resumes))
			r := streamRequest(streamBody)
			mutate(r.Header)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusBadRequest || submits.Load() != 0 || resumes.Load() != 0 {
				t.Fatalf("status=%d body=%q submit=%d resume=%d", w.Code, w.Body.String(), submits.Load(), resumes.Load())
			}
		})
	}

	for name, key := range map[string]string{
		"sixteen":          "0123456789abcdef",
		"one-twenty-eight": strings.Repeat("~", 128),
	} {
		t.Run(name, func(t *testing.T) {
			var submits, resumes atomic.Int64
			h, _ := New(token, 1, streamServices(&submits, &resumes))
			r := streamRequest(streamBody)
			r.Header.Set("Idempotency-Key", key)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusOK || submits.Load() != 1 || resumes.Load() != 0 {
				t.Fatalf("status=%d body=%q submit=%d resume=%d", w.Code, w.Body.String(), submits.Load(), resumes.Load())
			}
		})
	}
}

func TestTaskStreamInvalidRequestsNeverAdmitOrResume(t *testing.T) {
	bodies := []string{
		`null`,
		`{}`,
		`{"model_id":"m","prompt":null}`,
		`{"model_id":"m","prompt":"hello","unknown":"private"}`,
		`{"model_id":"m","model_id":"other","prompt":"hello"}`,
		`{"Model_ID":"m","prompt":"hello"}`,
		streamBody + `{}`,
		`{"model_id":"m","prompt":"hello","max_cost":"1"}`,
		`{"model_id":"m","prompt":"hello","summary_attempt_id":"draft"}`,
		`{"model_id":"m","prompt":"hello","continue_task_id":"task","summary_attempt_id":"draft","compaction":{"keep":1,"summary":{"decisions":["entry"]}}}`,
		`{"model_id":"m","prompt":"` + strings.Repeat("x", 1<<20) + `"}`,
	}
	for i, body := range bodies {
		for _, resume := range []bool{false, true} {
			t.Run(strconv.Itoa(i)+"/resume="+strconv.FormatBool(resume), func(t *testing.T) {
				var submits, resumes atomic.Int64
				h, _ := New(token, 1, streamServices(&submits, &resumes))
				r := streamRequest(body)
				if resume {
					r.Header.Set("Last-Event-ID", "submission:0")
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != http.StatusBadRequest || submits.Load() != 0 || resumes.Load() != 0 || !json.Valid(w.Body.Bytes()) || strings.Contains(w.Body.String(), "private") {
					t.Fatalf("status=%d submit=%d resume=%d body=%q", w.Code, submits.Load(), resumes.Load(), w.Body.String())
				}
			})
		}
	}
}

func TestTaskStreamFramesUseSubmissionWideDurableCursor(t *testing.T) {
	var submits, resumes atomic.Int64
	h, err := New(token, 1, streamServices(&submits, &resumes))
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, streamRequest(streamBody))
	if w.Code != http.StatusOK || !w.Flushed || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatal(w.Code, w.Header())
	}
	frames := parseStreamFrames(t, w.Body.String())
	if len(frames) != 3 || submits.Load() != 1 || resumes.Load() != 0 {
		t.Fatal(frames, submits.Load(), resumes.Load())
	}
	if frames[0].event != string(runtime.TaskStarted) || frames[0].id != "submission:1" || frames[1].event != string(runtime.TaskCompleted) || frames[1].id != "submission:2" || frames[2].event != "result" || frames[2].id != "submission:3" {
		t.Fatal(frames)
	}
	var result map[string]any
	if json.Unmarshal([]byte(frames[2].data), &result) != nil || result["submission_id"] != "submission" || result["text"] != "answer" {
		t.Fatal(result)
	}
}

func TestTaskStreamResumeNeverAdmitsAndDoesNotDuplicateFrames(t *testing.T) {
	for _, after := range []int64{1, 2, 3} {
		var submits, resumes atomic.Int64
		h, _ := New(token, 1, streamServices(&submits, &resumes))
		r := streamRequest(streamBody)
		r.Header.Set("Last-Event-ID", "submission:"+strconv.FormatInt(after, 10))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK || submits.Load() != 0 || resumes.Load() != 1 {
			t.Fatal(after, w.Code, submits.Load(), resumes.Load(), w.Body.String())
		}
		frames := parseStreamFrames(t, w.Body.String())
		if len(frames) != int(3-after) {
			t.Fatal(after, frames)
		}
		for _, frame := range frames {
			_, n, _ := strings.Cut(frame.id, ":")
			if frame.id != "" && n <= strconv.FormatInt(after, 10) {
				t.Fatal("duplicate cursor", after, frame)
			}
		}
	}
}

func TestTaskStreamDeliversMultiplePagesWithContiguousCursors(t *testing.T) {
	const eventCount = 105
	status := streamStatus()
	events := make([]runtime.Event, eventCount)
	for i := range events {
		sequence := int64(i + 1)
		events[i] = streamEvent(sequence, runtime.ModelDelta)
		events[i].ID = "event-" + strconv.FormatInt(sequence, 10)
		events[i].TurnID = "turn"
	}
	events[0] = streamEvent(1, runtime.TaskStarted)
	events[0].ID = "event-1"
	events[0].Data.SubmissionID = status.ID
	events[eventCount-1] = streamEvent(eventCount, runtime.TaskCompleted)
	events[eventCount-1].ID = "event-105"

	var submits, resumes, reads atomic.Int64
	s := streamServices(&submits, &resumes)
	s.SubmissionStream = func(_ context.Context, id string, after int64, limit int) (submissions.StreamPage, error) {
		reads.Add(1)
		if id != status.ID || after < 0 || after > eventCount+1 || limit != 100 {
			return submissions.StreamPage{}, errors.New("invalid page request")
		}
		page := submissions.StreamPage{Version: 1, SubmissionID: id, FromSequence: after, NextSequence: after, EventHeadSequence: eventCount, ResultSequence: eventCount + 1, Status: status, Events: []submissions.StreamEvent{}}
		end := after + int64(limit)
		if end > eventCount {
			end = eventCount
		}
		for sequence := after + 1; sequence <= end; sequence++ {
			page.Events = append(page.Events, submissions.StreamEvent{Sequence: sequence, Event: events[sequence-1]})
			page.NextSequence = sequence
		}
		page.HasMoreEvents = page.NextSequence < page.EventHeadSequence
		return page, nil
	}
	h, _ := New(token, 1, s)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, streamRequest(streamBody))
	if w.Code != http.StatusOK || submits.Load() != 1 || resumes.Load() != 0 || reads.Load() != 2 {
		t.Fatalf("status=%d submit=%d resume=%d reads=%d", w.Code, submits.Load(), resumes.Load(), reads.Load())
	}
	frames := parseStreamFrames(t, w.Body.String())
	if len(frames) != eventCount+1 {
		t.Fatal("wrong frame count", len(frames))
	}
	for i, frame := range frames {
		sequence := i + 1
		if frame.id != "submission:"+strconv.Itoa(sequence) {
			t.Fatalf("frame %d cursor=%q", i, frame.id)
		}
		if i < eventCount && frame.event == "result" {
			t.Fatalf("early result at frame %d", i)
		}
	}
	if frames[eventCount].event != "result" {
		t.Fatal("missing single terminal result", frames[eventCount])
	}
}

func TestTaskStreamConflictForeignAndAheadCursorFailBeforeSSE(t *testing.T) {
	for _, condition := range []string{"request", "resume", "foreign", "ahead"} {
		var submits, resumes atomic.Int64
		s := streamServices(&submits, &resumes)
		if condition == "request" {
			s.Submit = func(context.Context, string, app.Request) (submissions.Status, error) {
				return submissions.Status{}, submissions.ErrConflict
			}
		}
		if condition == "resume" {
			s.ResumeSubmission = func(context.Context, string, app.Request) (submissions.Status, error) {
				return submissions.Status{}, submissions.ErrConflict
			}
		}
		h, _ := New(token, 1, s)
		r := streamRequest(streamBody)
		if condition != "request" {
			id := "submission"
			if condition == "foreign" {
				id = "foreign"
			}
			sequence := "1"
			if condition == "ahead" {
				sequence = "4"
			}
			r.Header.Set("Last-Event-ID", id+":"+sequence)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusConflict || strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") || !json.Valid(w.Body.Bytes()) {
			t.Fatal(condition, w.Code, w.Header(), w.Body.String())
		}
	}
}

func TestTaskStreamObservationFailureAfterHeadersDoesNotInventResult(t *testing.T) {
	for _, mode := range []string{"reader-error", "invalid-page"} {
		t.Run(mode, func(t *testing.T) {
			var submits, resumes, reads atomic.Int64
			status := streamStatus()
			status.State, status.Result = "running", nil
			s := streamServices(&submits, &resumes)
			s.Submit = func(context.Context, string, app.Request) (submissions.Status, error) {
				submits.Add(1)
				return status, nil
			}
			s.SubmissionStream = func(context.Context, string, int64, int) (submissions.StreamPage, error) {
				call := reads.Add(1)
				if call == 1 {
					start := streamEvent(1, runtime.TaskStarted)
					start.Data.SubmissionID = status.ID
					return submissions.StreamPage{Version: 1, SubmissionID: status.ID, FromSequence: 0, NextSequence: 1, EventHeadSequence: 1, Status: status, Events: []submissions.StreamEvent{{Sequence: 1, Event: start}}}, nil
				}
				if mode == "reader-error" {
					return submissions.StreamPage{}, errors.New("private observation failure")
				}
				return submissions.StreamPage{Version: 99}, nil
			}
			h, _ := New(token, 1, s)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, streamRequest(streamBody))
			frames := parseStreamFrames(t, w.Body.String())
			if w.Code != http.StatusOK || submits.Load() != 1 || resumes.Load() != 0 || reads.Load() != 2 || len(frames) != 1 || frames[0].event != string(runtime.TaskStarted) || strings.Contains(w.Body.String(), "event: result") || strings.Contains(w.Body.String(), "private observation failure") || len(h.slots) != 0 {
				t.Fatal(w.Code, submits.Load(), resumes.Load(), reads.Load(), frames, w.Body.String())
			}
		})
	}
}

type failingStreamWriter struct {
	header     http.Header
	writes     int
	panicWrite bool
	attempted  [][]byte
}

func (w *failingStreamWriter) Header() http.Header { return w.header }
func (*failingStreamWriter) WriteHeader(int)       {}
func (*failingStreamWriter) Flush()                {}
func (w *failingStreamWriter) Write(body []byte) (int, error) {
	w.writes++
	w.attempted = append(w.attempted, append([]byte(nil), body...))
	if w.panicWrite {
		panic("private-writer-detail")
	}
	return 0, io.ErrClosedPipe
}

type streamUnreadBody struct{ reads int }

func (b *streamUnreadBody) Read([]byte) (int, error) {
	b.reads++
	return 0, errors.New("body must not be read")
}
func (*streamUnreadBody) Close() error { return nil }

func TestTaskStreamWriterFailureStopsDeliveryOnly(t *testing.T) {
	for _, panicWrite := range []bool{false, true} {
		t.Run(strconv.FormatBool(panicWrite), func(t *testing.T) {
			var submits, resumes atomic.Int64
			h, _ := New(token, 1, streamServices(&submits, &resumes))
			w := &failingStreamWriter{header: http.Header{}, panicWrite: panicWrite}
			h.ServeHTTP(w, streamRequest(streamBody))
			if submits.Load() != 1 || resumes.Load() != 0 || w.writes != 1 || len(h.slots) != 0 || len(w.attempted) != 1 {
				t.Fatal(submits.Load(), resumes.Load(), w.writes, len(h.slots), len(w.attempted))
			}
			attempt := string(w.attempted[0])
			if strings.HasPrefix(strings.TrimSpace(attempt), "{") || strings.Contains(attempt, "event: result") || strings.Contains(attempt, "internal_error") || strings.Contains(attempt, "private-writer-detail") {
				t.Fatalf("writer failure appended fallback output: %q", attempt)
			}
		})
	}
}

type deadlineStreamRecorder struct {
	*httptest.ResponseRecorder
	deadline       time.Time
	deadlines      []time.Time
	flushDeadlines []time.Time
	operations     []string
}

func (w *deadlineStreamRecorder) SetWriteDeadline(deadline time.Time) error {
	w.deadline = deadline
	w.deadlines = append(w.deadlines, deadline)
	if deadline.IsZero() {
		w.operations = append(w.operations, "clear")
	} else {
		w.operations = append(w.operations, "set")
	}
	return nil
}

func (w *deadlineStreamRecorder) Flush() {
	w.flushDeadlines = append(w.flushDeadlines, w.deadline)
	w.operations = append(w.operations, "flush")
	w.ResponseRecorder.Flush()
}

func TestTaskStreamWriteDeadlinesBoundEveryFlushAndClearAfterDelivery(t *testing.T) {
	var submits, resumes atomic.Int64
	h, _ := New(token, 1, streamServices(&submits, &resumes))
	w := &deadlineStreamRecorder{ResponseRecorder: httptest.NewRecorder()}
	started := time.Now()
	h.ServeHTTP(w, streamRequest(streamBody))
	if w.Code != http.StatusOK || submits.Load() != 1 || resumes.Load() != 0 || len(w.flushDeadlines) != 4 || w.deadline != (time.Time{}) || len(w.deadlines) == 0 || w.operations[len(w.operations)-1] != "clear" {
		t.Fatal(w.Code, submits.Load(), resumes.Load(), len(w.flushDeadlines), w.deadline, w.operations)
	}
	for i, deadline := range w.flushDeadlines {
		if deadline.IsZero() || !deadline.After(started) || deadline.After(time.Now().Add(16*time.Second)) {
			t.Fatal("flush lacked a bounded active deadline", i, deadline)
		}
	}
	if frames := parseStreamFrames(t, w.Body.String()); len(frames) != 3 || frames[2].event != "result" {
		t.Fatal(frames)
	}
	wantFlushes := []string{"flush", "flush", "flush", "flush"}
	var gotFlushes []string
	for _, operation := range w.operations {
		if operation == "flush" {
			gotFlushes = append(gotFlushes, operation)
		}
	}
	if !reflect.DeepEqual(gotFlushes, wantFlushes) {
		t.Fatal(w.operations)
	}
}

func TestTaskStreamPageValidationRejectsCorruptData(t *testing.T) {
	var submits, resumes atomic.Int64
	s := streamServices(&submits, &resumes)
	s.SubmissionStream = func(context.Context, string, int64, int) (submissions.StreamPage, error) {
		page, _ := streamPage(streamStatus(), 0)
		page.Events[1].Sequence = 9
		return page, nil
	}
	h, _ := New(token, 1, s)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, streamRequest(streamBody))
	if w.Code != http.StatusInternalServerError || w.Body.String() != "{\"error\":\"invalid_stream_page\"}\n" {
		t.Fatal(w.Code, w.Body.String())
	}
}
