package api

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
)

const feedbackBody = `{"task_id":"task","outcome":"accepted","attempt_cost":0.25}`

func TestFeedbackDispatchAndErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
	}{
		{"accepted", nil, 200}, {"rejected", nil, 200},
		{"conflict", telemetry.ErrConflict, 409}, {"admission", app.ErrAdmission, 422},
		{"internal", errors.New("private secret"), 500}, {"panic", nil, 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := services()
			calls := 0
			s.Feedback = func(ctx context.Context, task string, accepted bool, cost float64) error {
				calls++
				if task != "task" || accepted != (tc.name != "rejected") || cost != .25 {
					t.Fatal("incorrect feedback arguments")
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("missing deadline")
				}
				if tc.name == "panic" {
					panic("private secret")
				}
				return tc.err
			}
			h, _ := New(token, 1, s)
			body := feedbackBody
			if tc.name == "rejected" {
				body = strings.ReplaceAll(body, "accepted", "rejected")
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request("POST", "/v1/feedback", body))
			if w.Code != tc.status || calls != 1 || strings.Contains(w.Body.String(), "private") {
				t.Fatalf("status %d calls %d: %s", w.Code, calls, w.Body.String())
			}
		})
	}
}

func TestFeedbackStrictRequest(t *testing.T) {
	for _, body := range []string{
		`{}`, `null`, `[]`, feedbackBody + `{}`,
		`{"task_id":null,"outcome":"accepted","attempt_cost":0}`,
		`{"task_id":1,"outcome":"accepted","attempt_cost":0}`,
		`{"task_id":" ","outcome":"accepted","attempt_cost":0}`,
		`{"task_id":"task","outcome":null,"attempt_cost":0}`,
		`{"task_id":"task","outcome":true,"attempt_cost":0}`,
		`{"task_id":"task","outcome":"success","attempt_cost":0}`,
		`{"task_id":"task","outcome":"accepted"}`,
		`{"task_id":"task","outcome":"accepted","attempt_cost":null}`,
		`{"task_id":"task","outcome":"accepted","attempt_cost":"0"}`,
		`{"task_id":"task","outcome":"accepted","attempt_cost":true}`,
		`{"task_id":"task","outcome":"accepted","attempt_cost":-1}`,
		`{"task_id":"task","outcome":"accepted","attempt_cost":1e999}`,
		strings.TrimSuffix(feedbackBody, "}") + `,"task_id":"task"}`,
		strings.TrimSuffix(feedbackBody, "}") + `,"outcome":"rejected"}`,
		strings.TrimSuffix(feedbackBody, "}") + `,"attempt_cost":0}`,
		strings.TrimSuffix(feedbackBody, "}") + `,"unknown":true}`,
		strings.ReplaceAll(feedbackBody, "task_id", "Task_ID"),
	} {
		t.Run(body, func(t *testing.T) {
			s := services()
			s.Feedback = func(context.Context, string, bool, float64) error { t.Fatal("invalid request dispatched"); return nil }
			h, _ := New(token, 1, s)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request("POST", "/v1/feedback", body))
			if w.Code != 400 {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
		})
	}
	if _, _, cost, err := decodeFeedback([]byte(strings.ReplaceAll(feedbackBody, "0.25", "0"))); err != nil || cost != 0 {
		t.Fatal("zero cost rejected", err)
	}
}

func TestFeedbackAdmission(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
	}{
		{"auth", 401}, {"origin", 403}, {"query", 400}, {"method", 404},
		{"media", 415}, {"oversize", 413}, {"capacity", 503}, {"unavailable", 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := services()
			if tc.name != "unavailable" {
				s.Feedback = func(context.Context, string, bool, float64) error { t.Fatal("denied request dispatched"); return nil }
			}
			h, _ := New(token, 1, s)
			body := feedbackBody
			if tc.name == "oversize" {
				body += strings.Repeat(" ", 4096)
			}
			r := request("POST", "/v1/feedback", body)
			switch tc.name {
			case "auth":
				r.Header.Del("Authorization")
			case "origin":
				r.Header.Set("Origin", "https://example.com")
			case "query":
				r.URL.RawQuery = "a=b"
			case "method":
				r.Method = "GET"
			case "media":
				r.Header.Set("Content-Type", "text/plain")
			case "capacity":
				h.slots <- struct{}{}
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestFeedbackCancellationAndSlotRelease(t *testing.T) {
	s := services()
	s.Feedback = func(ctx context.Context, _ string, _ bool, _ float64) error {
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Fatal("request cancellation lost")
		}
		return ctx.Err()
	}
	h, _ := New(token, 1, s)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := request("POST", "/v1/feedback", feedbackBody).WithContext(ctx)
	h.ServeHTTP(httptest.NewRecorder(), r)
	if len(h.slots) != 0 {
		t.Fatal("canceled feedback retained capacity")
	}
}
