package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func apiCancellationStatus(state string, requested bool) runtime.CancellationStatus {
	status := runtime.CancellationStatus{Version: 1, TaskID: "task", State: state, Requested: requested}
	if requested {
		at := time.Unix(100, 0).UTC()
		status.RequestID, status.RequestedAt = "cancellation-request", &at
	}
	return status
}

func TestCancellationEndpointsReturnStatusWithoutExecuting(t *testing.T) {
	for _, state := range []string{"running", "completed", "failed", "canceled"} {
		for _, requested := range []bool{false, true} {
			for _, method := range []string{"POST", "GET"} {
				s := services()
				s.Run = func(context.Context, app.Request) (app.Result, error) {
					t.Error("cancellation executed task")
					return app.Result{}, nil
				}
				s.RunStream = func(context.Context, app.Request, func(runtime.Event) error) (app.Result, error) {
					t.Error("cancellation streamed task")
					return app.Result{}, nil
				}
				status := apiCancellationStatus(state, requested)
				cancelCalls, readCalls := 0, 0
				s.Cancel = func(_ context.Context, task string) (runtime.CancellationStatus, error) {
					cancelCalls++
					if task != "task" {
						t.Error(task)
					}
					return status, nil
				}
				s.Cancellation = func(_ context.Context, task string) (runtime.CancellationStatus, error) {
					readCalls++
					if task != "task" {
						t.Error(task)
					}
					return status, nil
				}
				h, err := New(token, 1, s)
				if err != nil {
					t.Fatal(err)
				}
				path, want := "/v1/tasks/task/cancel", 200
				if method == "GET" {
					path = "/v1/tasks/task/cancellation"
				} else if state == "running" && requested {
					want = 202
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, request(method, path, `{}`))
				var got runtime.CancellationStatus
				if w.Code != want || json.Unmarshal(w.Body.Bytes(), &got) != nil || !reflect.DeepEqual(got, status) {
					t.Fatal(method, state, requested, w.Code, w.Body.String())
				}
				if method == "GET" && (cancelCalls != 0 || readCalls != 1) || method == "POST" && (cancelCalls != 1 || readCalls != 0) {
					t.Fatal("wrong cancellation operation", method, cancelCalls, readCalls)
				}
			}
		}
	}
}

func TestCancellationAdmissionAndMissingHooks(t *testing.T) {
	for _, method := range []string{"POST", "GET"} {
		for _, condition := range []string{"auth", "origin", "query", "missing-hook"} {
			s := services()
			calls := 0
			hook := func(context.Context, string) (runtime.CancellationStatus, error) {
				calls++
				return apiCancellationStatus("running", true), nil
			}
			s.Cancel, s.Cancellation = hook, hook
			if condition == "missing-hook" {
				s.Cancel, s.Cancellation = nil, nil
			}
			h, _ := New(token, 1, s)
			path := "/v1/tasks/task/cancel"
			if method == "GET" {
				path = "/v1/tasks/task/cancellation"
			}
			r := request(method, path, `{}`)
			want := 503
			switch condition {
			case "auth":
				r.Header.Del("Authorization")
				want = 401
			case "origin":
				r.Header.Set("Origin", "https://example.com")
				want = 403
			case "query":
				r.URL.RawQuery = "token=private-query"
				want = 400
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != want || calls != 0 || strings.Contains(w.Body.String(), "private-query") {
				t.Fatal(method, condition, w.Code, calls, w.Body.String())
			}
		}
	}
}

func TestCancellationRejectsBodyExtensionsAndInvalidTaskIDs(t *testing.T) {
	for _, body := range []string{"", "null", "[]", `{"request_id":"caller"}`, `{"reason":null}`, `{"reason":"a","reason":"b"}`, "{} {}", "{} trailing", "{}" + strings.Repeat(" ", 1023)} {
		s := services()
		s.Cancel = func(context.Context, string) (runtime.CancellationStatus, error) {
			t.Error("invalid body dispatched")
			return apiCancellationStatus("running", true), nil
		}
		h, _ := New(token, 1, s)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request("POST", "/v1/tasks/task/cancel", body))
		want := 400
		if len(body) > 1024 {
			want = 413
		}
		if w.Code != want {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	s := services()
	s.Cancel = func(context.Context, string) (runtime.CancellationStatus, error) {
		return apiCancellationStatus("running", true), nil
	}
	s.Cancellation = s.Cancel
	h, _ := New(token, 1, s)
	r := request("POST", "/v1/tasks/task/cancel", `{}`)
	r.Header.Set("Content-Type", "text/plain")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 415 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, method := range []string{"POST", "GET"} {
		for _, id := range []string{"", "task:other", "task/other", "task space", "task\nother", "task\xff", strings.Repeat("x", 129)} {
			r := request(method, "/v1/tasks/task/cancel", `{}`)
			suffix := "/cancel"
			if method == "GET" {
				suffix = "/cancellation"
			}
			r.URL.Path = "/v1/tasks/" + id + suffix
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 400 {
				t.Fatal(method, id, w.Code, w.Body.String())
			}
		}
	}
}

func TestCancellationErrorsAndInvalidHookStatusAreGeneric(t *testing.T) {
	for _, method := range []string{"POST", "GET"} {
		for _, tc := range []struct {
			err  error
			code int
		}{{sql.ErrNoRows, 404}, {errors.New("private-storage-detail"), 500}} {
			s := services()
			s.Cancel = func(context.Context, string) (runtime.CancellationStatus, error) {
				return runtime.CancellationStatus{}, tc.err
			}
			s.Cancellation = s.Cancel
			h, _ := New(token, 1, s)
			path := "/v1/tasks/task/cancel"
			if method == "GET" {
				path = "/v1/tasks/task/cancellation"
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request(method, path, `{}`))
			if w.Code != tc.code || strings.Contains(w.Body.String(), "private-storage-detail") {
				t.Fatal(method, w.Code, w.Body.String())
			}
		}
		for _, mutate := range []func(*runtime.CancellationStatus){
			func(s *runtime.CancellationStatus) { s.Version = 2 },
			func(s *runtime.CancellationStatus) { s.TaskID = "other" },
			func(s *runtime.CancellationStatus) { s.State = "unknown" },
			func(s *runtime.CancellationStatus) { s.RequestID = "" },
			func(s *runtime.CancellationStatus) { s.RequestedAt = nil },
			func(s *runtime.CancellationStatus) { s.RequestedAt = new(time.Time) },
			func(s *runtime.CancellationStatus) { s.Requested = false },
		} {
			status := apiCancellationStatus("running", true)
			mutate(&status)
			s := services()
			s.Cancel = func(context.Context, string) (runtime.CancellationStatus, error) { return status, nil }
			s.Cancellation = s.Cancel
			h, _ := New(token, 1, s)
			path := "/v1/tasks/task/cancel"
			if method == "GET" {
				path = "/v1/tasks/task/cancellation"
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request(method, path, `{}`))
			if w.Code != 500 {
				t.Fatal("invalid status returned", method, status, w.Code, w.Body.String())
			}
		}
	}
}

func TestCancellationRemainsAvailableAtTaskCapacityAndIsTaskScoped(t *testing.T) {
	s := services()
	status := apiCancellationStatus("running", true)
	calls := 0
	s.Cancel = func(_ context.Context, id string) (runtime.CancellationStatus, error) {
		calls++
		if id != "task" {
			t.Error(id)
		}
		return status, nil
	}
	h, _ := New(token, 1, s)
	h.slots <- struct{}{}
	defer func() { <-h.slots }()
	var responses []string
	for range 2 {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request("POST", "/v1/tasks/task/cancel", `{}`))
		if w.Code != 202 {
			t.Fatal("task capacity blocked cancellation", w.Code, w.Body.String())
		}
		responses = append(responses, w.Body.String())
	}
	if calls != 2 || responses[0] != responses[1] {
		t.Fatal("task-scoped status changed across repeat", calls, responses)
	}
}

func TestCancellationControlCapacityRejectsBeforeBody(t *testing.T) {
	s := services()
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var calls atomic.Int32
	s.Cancel = func(context.Context, string) (runtime.CancellationStatus, error) {
		calls.Add(1)
		started <- struct{}{}
		<-release
		return apiCancellationStatus("running", true), nil
	}
	h, _ := New(token, 1, s)
	var workers sync.WaitGroup
	defer func() { close(release); workers.Wait() }()
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			h.ServeHTTP(httptest.NewRecorder(), request("POST", "/v1/tasks/task/cancel", `{}`))
		}()
	}
	for range 2 {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("two independent control slots unavailable")
		}
	}
	r := request("POST", "/v1/tasks/task/cancel", "")
	body := &streamUnreadBody{}
	r.Body = body
	w := httptest.NewRecorder()
	done := make(chan struct{})
	workers.Add(1)
	go func() {
		defer workers.Done()
		defer close(done)
		h.ServeHTTP(w, r)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("excess cancellation was not rejected promptly")
	}
	if w.Code != 503 || body.reads != 0 || calls.Load() != 2 {
		t.Fatal("control capacity read body or dispatched", w.Code, body.reads, calls.Load())
	}
}
