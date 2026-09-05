package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"darwinrouter/internal/app"
	"darwinrouter/runtime"
)

const streamBody = `{"model_id":"m","prompt":"hello"}`

func streamEvent(sequence int64, kind runtime.Kind) runtime.Event {
	return runtime.Event{Version: 1, ID: string(kind), TaskID: "task", SessionID: "session", Sequence: sequence, Time: time.Unix(100, 0).UTC(), Kind: kind, CorrelationID: "task"}
}

type streamFrame struct{ event, id, data string }

func parseStreamFrames(t *testing.T, body string) []streamFrame {
	t.Helper()
	if !strings.HasSuffix(body, "\n\n") {
		t.Fatalf("unterminated SSE frame: %q", body)
	}
	var frames []streamFrame
	for _, block := range strings.Split(strings.TrimSuffix(body, "\n\n"), "\n\n") {
		frame := streamFrame{}
		seen := map[string]bool{}
		for _, line := range strings.Split(block, "\n") {
			key, value, ok := strings.Cut(line, ": ")
			if !ok || seen[key] {
				t.Fatalf("invalid or duplicate SSE field: %q", line)
			}
			seen[key] = true
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
			t.Fatalf("invalid SSE event: %+v", frame)
		}
		frames = append(frames, frame)
	}
	return frames
}

func TestTaskStreamAuthenticationAndPreflight(t *testing.T) {
	for _, condition := range []string{"unauthorized", "origin", "query", "last-event-id", "media", "missing-hook"} {
		t.Run(condition, func(t *testing.T) {
			calls := 0
			s := services()
			s.RunStream = func(context.Context, app.Request, func(runtime.Event) error) (app.Result, error) {
				calls++
				return app.Result{}, nil
			}
			if condition == "missing-hook" {
				s.RunStream = nil
			}
			h, err := New(token, 1, s)
			if err != nil {
				t.Fatal(err)
			}
			r := request("POST", "/v1/tasks/stream", streamBody)
			want := 400
			switch condition {
			case "unauthorized":
				r.Header.Del("Authorization")
				want = 401
			case "origin":
				r.Header.Set("Origin", "https://example.com")
				want = 403
			case "query":
				r.URL.RawQuery = "secret=do-not-echo"
			case "last-event-id":
				r.Header.Set("Last-Event-ID", "task:1")
			case "media":
				r.Header.Set("Content-Type", "text/plain")
				want = 415
			case "missing-hook":
				want = 503
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != want || calls != 0 || !json.Valid(w.Body.Bytes()) || strings.Contains(w.Body.String(), "do-not-echo") {
				t.Fatal(w.Code, calls, w.Body.String())
			}
		})
	}
}

func TestTaskStreamStrictRequestValidation(t *testing.T) {
	for _, body := range []string{
		`null`, `{}`, `{"model_id":"m","prompt":null}`, `{"model_id":"m","prompt":"hello","unknown":"private"}`,
		`{"model_id":"m","model_id":"other","prompt":"hello"}`, `{"Model_ID":"m","prompt":"hello"}`,
		streamBody + `{}`, `{"model_id":"m","prompt":"hello","max_cost":"1"}`,
		`{"model_id":"m","prompt":"hello","summary_attempt_id":"draft"}`,
		`{"model_id":"m","prompt":"hello","continue_task_id":"task","summary_attempt_id":"draft","compaction":{"keep":1,"summary":{"decisions":["entry"]}}}`,
		`{"model_id":"m","prompt":"` + strings.Repeat("x", 1<<20) + `"}`,
	} {
		s := services()
		calls := 0
		s.RunStream = func(context.Context, app.Request, func(runtime.Event) error) (app.Result, error) {
			calls++
			return app.Result{}, nil
		}
		h, err := New(token, 1, s)
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request("POST", "/v1/tasks/stream", body))
		if w.Code != 400 || calls != 0 {
			t.Fatalf("invalid input dispatched: %d %d %s", w.Code, calls, w.Body.String())
		}
	}
}

type streamUnreadBody struct{ reads int }

func (b *streamUnreadBody) Read([]byte) (int, error) {
	b.reads++
	return 0, errors.New("body must not be read")
}
func (*streamUnreadBody) Close() error { return nil }

func TestTaskStreamCapacityIsCheckedBeforeBody(t *testing.T) {
	s := services()
	s.RunStream = func(context.Context, app.Request, func(runtime.Event) error) (app.Result, error) {
		t.Error("capacity request dispatched")
		return app.Result{}, nil
	}
	h, err := New(token, 1, s)
	if err != nil {
		t.Fatal(err)
	}
	h.slots <- struct{}{}
	defer func() { <-h.slots }()
	r := request("POST", "/v1/tasks/stream", "")
	body := &streamUnreadBody{}
	r.Body = body
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 503 || body.reads != 0 || w.Header().Get("Retry-After") == "" {
		t.Fatal(w.Code, body.reads, w.Body.String())
	}
}

func TestTaskStreamFramesPreserveEventOrderAndFinalResult(t *testing.T) {
	events := []runtime.Event{streamEvent(1, runtime.TaskStarted), streamEvent(2, runtime.TaskCompleted)}
	s := services()
	s.Run = func(context.Context, app.Request) (app.Result, error) {
		t.Error("synchronous service called")
		return app.Result{}, nil
	}
	s.RunStream = func(ctx context.Context, req app.Request, emit func(runtime.Event) error) (app.Result, error) {
		if req.ModelID != "m" || req.Prompt != "hello" {
			t.Fatal(req)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Error("stream lacks request deadline")
		}
		for _, event := range events {
			if err := emit(event); err != nil {
				return app.Result{}, err
			}
		}
		return app.Result{TaskID: "task", Text: "answer", Turns: 1, FinishReason: "stop", PreviousTaskIDs: []string{"prior"}}, nil
	}
	h, err := New(token, 1, s)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request("POST", "/v1/tasks/stream", streamBody))
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") || w.Header().Get("Cache-Control") != "no-store" || !w.Flushed {
		t.Fatal(w.Code, w.Header(), w.Flushed)
	}
	frames := parseStreamFrames(t, w.Body.String())
	if len(frames) != 3 {
		t.Fatal(frames)
	}
	for i, event := range events {
		var got runtime.Event
		if err := json.Unmarshal([]byte(frames[i].data), &got); err != nil || !reflect.DeepEqual(got, event) || frames[i].event != string(event.Kind) || frames[i].id != []string{"task:1", "task:2"}[i] {
			t.Fatal(frames[i], got, err)
		}
	}
	var final struct {
		TaskID       string   `json:"task_id"`
		Text         string   `json:"text"`
		Turns        int      `json:"turns"`
		FinishReason string   `json:"finish_reason"`
		Previous     []string `json:"previous_task_ids"`
		Error        string   `json:"error"`
	}
	if err := json.Unmarshal([]byte(frames[2].data), &final); err != nil || frames[2].event != "result" || frames[2].id != "" || final.TaskID != "task" || final.Text != "answer" || final.Turns != 1 || final.FinishReason != "stop" || len(final.Previous) != 1 || final.Previous[0] != "prior" || final.Error != "" {
		t.Fatal(frames[2], final, err)
	}
	if len(h.slots) != 0 {
		t.Fatal("stream leaked capacity slot")
	}
}

func TestTaskStreamErrorsAndPanicsStayGenericSSE(t *testing.T) {
	for _, condition := range []string{"admission", "service-error", "panic-before-event", "panic-after-event"} {
		t.Run(condition, func(t *testing.T) {
			s := services()
			s.RunStream = func(_ context.Context, _ app.Request, emit func(runtime.Event) error) (app.Result, error) {
				if condition == "panic-after-event" {
					if err := emit(streamEvent(1, runtime.TaskStarted)); err != nil {
						return app.Result{}, err
					}
				}
				if strings.HasPrefix(condition, "panic-") {
					panic("private-provider-detail")
				}
				if condition == "admission" {
					return app.Result{}, app.ErrAdmission
				}
				return app.Result{TaskID: "task", Text: "private-provider-detail"}, errors.New("private-provider-detail")
			}
			h, err := New(token, 1, s)
			if err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request("POST", "/v1/tasks/stream", streamBody))
			if w.Code != 200 || strings.Contains(w.Body.String(), "private-provider-detail") {
				t.Fatal(w.Code, w.Body.String())
			}
			frames := parseStreamFrames(t, w.Body.String())
			last := frames[len(frames)-1]
			var result map[string]any
			if json.Unmarshal([]byte(last.data), &result) != nil || last.event != "result" || result["error"] == nil || result["error"] == "" {
				t.Fatal(last)
			}
			if len(h.slots) != 0 {
				t.Fatal("failed stream leaked capacity")
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
func (w *failingStreamWriter) Write(b []byte) (int, error) {
	w.writes++
	w.attempted = append(w.attempted, append([]byte(nil), b...))
	if w.panicWrite {
		panic("private-writer-detail")
	}
	return 0, io.ErrClosedPipe
}

func TestTaskStreamSinkFailureDoesNotAppendJSONOrRetryWrites(t *testing.T) {
	for _, panics := range []bool{false, true} {
		s := services()
		observedError := false
		s.RunStream = func(_ context.Context, _ app.Request, emit func(runtime.Event) error) (app.Result, error) {
			err := emit(streamEvent(1, runtime.TaskStarted))
			observedError = err != nil
			return app.Result{}, err
		}
		h, err := New(token, 1, s)
		if err != nil {
			t.Fatal(err)
		}
		w := &failingStreamWriter{header: http.Header{}, panicWrite: panics}
		h.ServeHTTP(w, request("POST", "/v1/tasks/stream", streamBody))
		if !observedError || w.writes != 1 {
			t.Fatal("sink failure not propagated or wrote again", panics, observedError, w.writes)
		}
		for _, b := range w.attempted {
			if strings.HasPrefix(strings.TrimSpace(string(b)), "{") || strings.Contains(string(b), "private-writer-detail") {
				t.Fatal("ordinary JSON or panic payload written after streaming", string(b))
			}
		}
		if len(h.slots) != 0 {
			t.Fatal("sink failure leaked capacity")
		}
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

func TestTaskStreamWriteDeadlinesCoverFlushButNotModelWork(t *testing.T) {
	w := &deadlineStreamRecorder{ResponseRecorder: httptest.NewRecorder()}
	s := services()
	s.RunStream = func(_ context.Context, _ app.Request, emit func(runtime.Event) error) (app.Result, error) {
		// The initial header flush has completed before model work starts.
		if len(w.flushDeadlines) != 1 || !w.deadline.IsZero() {
			t.Fatal("initial flush left a deadline active during model work", w.operations)
		}
		for i, event := range []runtime.Event{streamEvent(1, runtime.TaskStarted), streamEvent(2, runtime.TaskCompleted)} {
			if !w.deadline.IsZero() {
				t.Fatal("deadline active before durable event emission")
			}
			if err := emit(event); err != nil {
				return app.Result{}, err
			}
			// Model work may take longer than a write deadline between events.
			if len(w.flushDeadlines) != i+2 || !w.deadline.IsZero() {
				t.Fatal("event flush left a deadline active after callback", w.operations)
			}
		}
		return app.Result{TaskID: "task", Text: "answer", Turns: 1}, nil
	}
	h, err := New(token, 1, s)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	h.ServeHTTP(w, request("POST", "/v1/tasks/stream", streamBody))
	if w.Code != 200 || len(w.flushDeadlines) != 4 || len(w.deadlines) != 8 || !w.deadline.IsZero() {
		t.Fatal("missing initial/event/final deadline cleanup", w.Code, w.operations)
	}
	for i, deadline := range w.flushDeadlines {
		if deadline.IsZero() || !deadline.After(start) || deadline.After(time.Now().Add(16*time.Second)) {
			t.Fatal("flush did not have a bounded active write deadline", i, deadline)
		}
	}
	want := []string{"set", "flush", "clear", "set", "flush", "clear", "set", "flush", "clear", "set", "flush", "clear"}
	if !reflect.DeepEqual(w.operations, want) {
		t.Fatal("deadline/flush ordering", w.operations)
	}
	if frames := parseStreamFrames(t, w.Body.String()); len(frames) != 3 || frames[2].event != "result" {
		t.Fatal("final result not delivered", frames)
	}
}
