package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"darwinrouter/internal/app"
	"darwinrouter/internal/telemetry"
	"darwinrouter/runtime"
)

func steeringStatusFixture() runtime.SteeringMessage {
	return runtime.SteeringMessage{Version: 1, ID: "steer", TaskID: "task", Text: "private guidance", State: "pending", CreatedAt: time.Now().UTC()}
}

func TestSteeringAPISafeMetadataAndIndependentCapacity(t *testing.T) {
	for _, method := range []string{"POST", "GET"} {
		s := services()
		calls := 0
		check := func(ctx context.Context) {
			calls++
			if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 5*time.Second {
				t.Error("unbounded control hook")
			}
		}
		s.Steer = func(ctx context.Context, task, key, text string) (runtime.SteeringMessage, error) {
			check(ctx)
			if task != "task" || key != "private-key" || text != "private guidance" {
				t.Error(task, key, text)
			}
			return steeringStatusFixture(), nil
		}
		s.Steering = func(ctx context.Context, task, id string) (runtime.SteeringMessage, error) {
			check(ctx)
			if task != "task" || id != "steer" {
				t.Error(task, id)
			}
			return steeringStatusFixture(), nil
		}
		s.Run = func(context.Context, app.Request) (app.Result, error) {
			t.Error("control executed task")
			return app.Result{}, nil
		}
		h, _ := New(token, 1, s)
		h.slots <- struct{}{}
		h.controls <- struct{}{}
		h.controls <- struct{}{}
		h.healthSlots <- struct{}{}
		h.metricsSlots <- struct{}{}
		path, body, want := "/v1/tasks/task/steering", `{"idempotency_key":"private-key","text":"private guidance"}`, 202
		if method == "GET" {
			path += "/steer"
			body = ""
			want = 200
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request(method, path, body))
		var out map[string]json.RawMessage
		if w.Code != want || calls != 1 || json.Unmarshal(w.Body.Bytes(), &out) != nil || len(out) != 5 || strings.Contains(w.Body.String(), "private") || out["text"] != nil || out["idempotency_key"] != nil || len(h.steeringSlots) != 0 {
			t.Fatal(w.Code, w.Body.String(), calls)
		}
	}
}

func TestSteeringAPIAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body string
		status                   int
		mutate                   func(*Handler, *http.Request)
	}{
		{"auth", "POST", "/v1/tasks/task/steering", `{}`, 401, func(_ *Handler, r *http.Request) { r.Header.Del("Authorization") }},
		{"origin", "POST", "/v1/tasks/task/steering", `{}`, 403, func(_ *Handler, r *http.Request) { r.Header.Set("Origin", "https://example.test") }},
		{"query", "POST", "/v1/tasks/task/steering?secret=private", `{}`, 400, nil},
		{"empty task", "POST", "/v1/tasks//steering", `{}`, 404, nil},
		{"nested task", "POST", "/v1/tasks/a/b/steering", `{}`, 404, nil},
		{"bad id", "GET", "/v1/tasks/task/steering/a:b", "", 404, nil},
		{"suffix", "POST", "/v1/tasks/task/steering-extra", `{}`, 404, nil},
		{"method", "DELETE", "/v1/tasks/task/steering", `{}`, 404, nil},
		{"get collection", "GET", "/v1/tasks/task/steering", "", 404, nil},
		{"post detail", "POST", "/v1/tasks/task/steering/id", `{}`, 404, nil},
		{"missing hook", "POST", "/v1/tasks/task/steering", `{}`, 503, func(h *Handler, _ *http.Request) { h.services.Steer = nil }},
		{"capacity", "POST", "/v1/tasks/task/steering", `{}`, 503, func(h *Handler, r *http.Request) {
			h.steeringSlots <- struct{}{}
			h.steeringSlots <- struct{}{}
			r.Body = metricsUnreadBody{t}
		}},
		{"media", "POST", "/v1/tasks/task/steering", `{}`, 415, func(_ *Handler, r *http.Request) { r.Header.Set("Content-Type", "text/plain") }},
		{"get body", "GET", "/v1/tasks/task/steering/steer", `{}`, 400, nil},
		{"chunked get", "GET", "/v1/tasks/task/steering/steer", "", 400, func(_ *Handler, r *http.Request) {
			r.TransferEncoding = []string{"chunked"}
			r.Body = metricsUnreadBody{t}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := services()
			s.Steer = func(context.Context, string, string, string) (runtime.SteeringMessage, error) {
				t.Error("invalid request dispatched")
				return steeringStatusFixture(), nil
			}
			s.Steering = func(context.Context, string, string) (runtime.SteeringMessage, error) {
				t.Error("invalid request dispatched")
				return steeringStatusFixture(), nil
			}
			h, _ := New(token, 1, s)
			r := request(tc.method, tc.path, tc.body)
			if tc.mutate != nil {
				tc.mutate(h, r)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}

func TestSteeringAPIRejectsMalformedBodies(t *testing.T) {
	for _, body := range []string{`{}`, `null`, `[]`, `{"text":"hello"}`, `{"idempotency_key":"k","text":"hello","extra":1}`, `{"idempotency_key":"k","text":"hello","text":"again"}`, `{"idempotency_key":"k","idempotency_key":"j","text":"hello"}`, `{"idempotency_key":1,"text":"hello"}`, `{"idempotency_key":"k","text":null}`, `{"idempotency_key":"k","text":" "}`, `{"idempotency_key":"k","text":"hello"}{}`, `{"idempotency_key":"` + strings.Repeat("k", 129) + `","text":"hello"}`, `{"idempotency_key":"k","text":"` + strings.Repeat("x", runtime.MaxSteeringBytes+1) + `"}`} {
		s := services()
		s.Steer = func(context.Context, string, string, string) (runtime.SteeringMessage, error) {
			t.Error("malformed body dispatched")
			return steeringStatusFixture(), nil
		}
		h, _ := New(token, 1, s)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request("POST", "/v1/tasks/task/steering", body))
		if w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}

func TestSteeringAPIErrorsAndInvalidHookStatus(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
	}{{sql.ErrNoRows, 404}, {runtime.ErrSteeringClosed, 409}, {runtime.ErrSteeringLimit, 429}, {telemetry.ErrConflict, 409}, {app.ErrAdmission, 400}, {errors.New("private backend error"), 500}} {
		s := services()
		s.Steer = func(context.Context, string, string, string) (runtime.SteeringMessage, error) {
			return steeringStatusFixture(), tc.err
		}
		h, _ := New(token, 1, s)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request("POST", "/v1/tasks/task/steering", `{"idempotency_key":"private-key","text":"private guidance"}`))
		if w.Code != tc.status || strings.Contains(w.Body.String(), "private") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	for _, mutate := range []func(*runtime.SteeringMessage){func(m *runtime.SteeringMessage) { m.TaskID = "other" }, func(m *runtime.SteeringMessage) { m.ID = "other" }, func(m *runtime.SteeringMessage) { m.Version = 2 }, func(m *runtime.SteeringMessage) { m.Text = "" }, func(m *runtime.SteeringMessage) { m.State = "private" }, func(m *runtime.SteeringMessage) { m.CreatedAt = time.Time{} }} {
		s := services()
		s.Steering = func(context.Context, string, string) (runtime.SteeringMessage, error) {
			m := steeringStatusFixture()
			mutate(&m)
			return m, nil
		}
		h, _ := New(token, 1, s)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request("GET", "/v1/tasks/task/steering/steer", ""))
		if w.Code != 500 || strings.Contains(w.Body.String(), "private") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}

func TestSteeringAPIAppliedIdempotentStatusAndOversize(t *testing.T) {
	s := services()
	s.Steer = func(context.Context, string, string, string) (runtime.SteeringMessage, error) {
		m := steeringStatusFixture()
		sequence := int64(4)
		m.State = "applied"
		m.AppliedSequence = &sequence
		return m, nil
	}
	h, _ := New(token, 1, s)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request("POST", "/v1/tasks/task/steering", `{"idempotency_key":"private-key","text":"private guidance"}`))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"applied_sequence":4`) || strings.Contains(w.Body.String(), "private") {
		t.Fatal(w.Code, w.Body.String())
	}
	s.Steer = func(context.Context, string, string, string) (runtime.SteeringMessage, error) {
		t.Error("oversize dispatched")
		return steeringStatusFixture(), nil
	}
	h, _ = New(token, 1, s)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, request("POST", "/v1/tasks/task/steering", strings.Repeat(" ", 512<<10+1)))
	if w.Code != 413 {
		t.Fatal(w.Code, w.Body.String())
	}
}
