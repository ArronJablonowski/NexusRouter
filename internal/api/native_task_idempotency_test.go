package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

func nativeTaskStatus(id, text string) submissions.Status {
	now := time.Unix(100, 0).UTC()
	return submissions.Status{
		Version:      1,
		ID:           "submission",
		State:        "succeeded",
		CreatedAt:    now,
		UpdatedAt:    now,
		ConfigDigest: strings.Repeat("a", 64),
		TaskIDs:      []string{id},
		Result: &submissions.Result{
			TaskID:       id,
			Text:         text,
			Turns:        1,
			FinishReason: "stop",
		},
	}
}

func nativeTaskRequest(body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/v1/tasks", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	return r
}

func TestNativeTaskStrictIdempotencyKeyAndNoDirectRunFallback(t *testing.T) {
	var direct, durable atomic.Int64
	s := services()
	s.Run = func(context.Context, app.Request) (app.Result, error) {
		direct.Add(1)
		return app.Result{TaskID: "direct", Text: "must not run", Turns: 1}, nil
	}
	s.RunSubmission = func(context.Context, string, app.Request) (submissions.Status, error) {
		durable.Add(1)
		return nativeTaskStatus("durable", "answer"), nil
	}
	h, err := New(token, 1, s)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"model_id":"model","prompt":"hello"}`
	tests := []struct {
		name string
		set  func(http.Header)
	}{
		{name: "missing"},
		{name: "short", set: func(h http.Header) { h.Set("Idempotency-Key", "too-short") }},
		{name: "space", set: func(h http.Header) { h.Set("Idempotency-Key", "sixteen bytes bad key") }},
		{name: "too-long", set: func(h http.Header) { h.Set("Idempotency-Key", strings.Repeat("x", 129)) }},
		{name: "duplicate", set: func(h http.Header) { h["Idempotency-Key"] = []string{"0123456789abcdef", "0123456789abcdef"} }},
		{name: "case-aliased", set: func(h http.Header) {
			h["Idempotency-Key"] = []string{"0123456789abcdef"}
			h["idempotency-key"] = []string{"0123456789abcdef"}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := nativeTaskRequest(body)
			if tc.set != nil {
				tc.set(r.Header)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusBadRequest || w.Body.String() != "{\"error\":\"invalid_idempotency_key\"}\n" {
				t.Fatalf("status/body = %d %q", w.Code, w.Body.String())
			}
		})
	}
	if direct.Load() != 0 || durable.Load() != 0 {
		t.Fatalf("invalid keys dispatched work: direct=%d durable=%d", direct.Load(), durable.Load())
	}

	s.RunSubmission = nil
	h, err = New(token, 1, s)
	if err != nil {
		t.Fatal(err)
	}
	r := nativeTaskRequest(body)
	r.Header.Set("Idempotency-Key", "0123456789abcdef")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable || w.Body.String() != "{\"error\":\"durable_tasks_unavailable\"}\n" || direct.Load() != 0 {
		t.Fatalf("missing durable path fell back: status=%d body=%q direct=%d", w.Code, w.Body.String(), direct.Load())
	}
}

func TestNativeTaskSameKeySameBodyAndChangedBodyConflict(t *testing.T) {
	var direct atomic.Int64
	var mu sync.Mutex
	var firstKey string
	var firstRequest app.Request
	var calls int
	s := services()
	s.Run = func(context.Context, app.Request) (app.Result, error) {
		direct.Add(1)
		return app.Result{}, nil
	}
	s.RunSubmission = func(_ context.Context, key string, request app.Request) (submissions.Status, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if firstKey == "" {
			firstKey, firstRequest = key, request
		} else if key != firstKey || !reflect.DeepEqual(request, firstRequest) {
			return submissions.Status{}, submissions.ErrConflict
		}
		return nativeTaskStatus("task", "answer"), nil
	}
	h, err := New(token, 1, s)
	if err != nil {
		t.Fatal(err)
	}
	call := func(body string) *httptest.ResponseRecorder {
		r := nativeTaskRequest(body)
		r.Header.Set("Idempotency-Key", "same-request-key-0001")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	body := `{"model_id":"model","prompt":"hello"}`
	first, second := call(body), call(body)
	if first.Code != http.StatusCreated || second.Code != http.StatusCreated || first.Body.String() != second.Body.String() {
		t.Fatalf("exact retry mismatch: first=%d %q second=%d %q", first.Code, first.Body.String(), second.Code, second.Body.String())
	}
	changed := call(`{"model_id":"model","prompt":"changed"}`)
	if changed.Code != http.StatusConflict || changed.Body.String() != "{\"error\":\"task_conflict\"}\n" {
		t.Fatalf("changed request = %d %q", changed.Code, changed.Body.String())
	}
	if calls != 3 || direct.Load() != 0 {
		t.Fatalf("calls=%d direct=%d", calls, direct.Load())
	}
}

func TestNativeTaskValidatesStatusBeforeResponse(t *testing.T) {
	s := services()
	s.Run = func(context.Context, app.Request) (app.Result, error) {
		t.Fatal("direct Run must not be used")
		return app.Result{}, nil
	}
	s.RunSubmission = func(context.Context, string, app.Request) (submissions.Status, error) {
		status := nativeTaskStatus("task", "private result must not escape")
		status.Result.TaskID = "unbound-task"
		return status, nil
	}
	h, err := New(token, 1, s)
	if err != nil {
		t.Fatal(err)
	}
	r := nativeTaskRequest(`{"model_id":"model","prompt":"hello"}`)
	r.Header.Set("Idempotency-Key", "validate-status-0001")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusInternalServerError || w.Body.String() != "{\"error\":\"invalid_submission_status\"}\n" || strings.Contains(w.Body.String(), "private") {
		t.Fatalf("unsafe invalid status response: %d %q", w.Code, w.Body.String())
	}

	s.RunSubmission = func(context.Context, string, app.Request) (submissions.Status, error) {
		return nativeTaskStatus("task", "answer"), nil
	}
	h, err = New(token, 1, s)
	if err != nil {
		t.Fatal(err)
	}
	r = nativeTaskRequest(`{"model_id":"model","prompt":"hello"}`)
	r.Header.Set("Idempotency-Key", "validate-status-0002")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var out map[string]json.RawMessage
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &out) != nil || string(out["task_id"]) != `"task"` || string(out["text"]) != `"answer"` || string(out["submission_id"]) != `"submission"` {
		t.Fatalf("valid response: %d %q", w.Code, w.Body.String())
	}
}

func TestNativeTaskRejectsNonterminalAndCorruptDurableStatus(t *testing.T) {
	s := services()
	s.RunSubmission = func(context.Context, string, app.Request) (submissions.Status, error) {
		status := nativeTaskStatus("task", "private result must not escape")
		status.State = "running"
		status.Result = nil
		return status, nil
	}
	h, err := New(token, 1, s)
	if err != nil {
		t.Fatal(err)
	}
	request := func(key string) *httptest.ResponseRecorder {
		r := nativeTaskRequest(`{"model_id":"model","prompt":"hello"}`)
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := request("nonterminal-status-0001")
	if w.Code != http.StatusInternalServerError || w.Body.String() != "{\"error\":\"invalid_submission_status\"}\n" {
		t.Fatalf("nonterminal status: %d %q", w.Code, w.Body.String())
	}
	s.RunSubmission = func(context.Context, string, app.Request) (submissions.Status, error) {
		return submissions.Status{}, submissions.ErrInvalid
	}
	h, err = New(token, 1, s)
	if err != nil {
		t.Fatal(err)
	}
	w = request("corrupt-status-0001")
	if w.Code != http.StatusInternalServerError || w.Body.String() != "{\"error\":\"task_unavailable\"}\n" {
		t.Fatalf("corrupt durable status: %d %q", w.Code, w.Body.String())
	}
}

func TestNativeTaskInternalWaitDeadlineReturnsDurableAcceptedStatus(t *testing.T) {
	s := services()
	s.RunSubmission = func(ctx context.Context, _ string, _ app.Request) (submissions.Status, error) {
		<-ctx.Done()
		status := nativeTaskStatus("task", "must not escape")
		status.State = "running"
		status.Result = nil
		return status, ctx.Err()
	}
	h, err := New(token, 1, s)
	if err != nil {
		t.Fatal(err)
	}
	h.taskWaitTimeout = 10 * time.Millisecond
	r := nativeTaskRequest(`{"model_id":"model","prompt":"hello"}`)
	r.Header.Set("Idempotency-Key", "deadline-status-0001")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusAccepted || w.Header().Get("Retry-After") != "1" || w.Body.String() != "{\"state\":\"running\",\"submission_id\":\"submission\",\"task_ids\":[\"task\"]}\n" {
		t.Fatalf("deadline status: %d %#v %q", w.Code, w.Header(), w.Body.String())
	}
}
