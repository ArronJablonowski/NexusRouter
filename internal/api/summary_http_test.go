package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

const httpSummaryBody = `{"task_id":"task","model_id":"model","keep":2,"max_cost":0.25}`
const httpReviewBody = `{"attempt_id":"attempt","expected_id":"previous","decision":"approved","note":"Checked source"}`

func httpSummaryServices() Services {
	s := services()
	s.Summarize = func(context.Context, string, string, int, float64) (sessions.SummaryAttempt, error) {
		return sessions.SummaryAttempt{ID: "attempt", Status: "drafted"}, nil
	}
	s.SummaryAttempt = func(context.Context, string) (sessions.SummaryAttempt, error) {
		return sessions.SummaryAttempt{ID: "attempt"}, nil
	}
	s.SummaryAttempts = func(context.Context, string, string, int) ([]sessions.SummaryAttempt, error) {
		return []sessions.SummaryAttempt{}, nil
	}
	s.ReviewSummary = func(context.Context, string, string, string, string) (sessions.SummaryReview, error) {
		return sessions.SummaryReview{ID: "review"}, nil
	}
	s.SummaryReviews = func(context.Context, string) ([]sessions.SummaryReview, error) {
		return []sessions.SummaryReview{}, nil
	}
	return s
}

func TestHTTPSummaryArgumentForwarding(t *testing.T) {
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{"POST", "/v1/summaries", httpSummaryBody, 201},
		{"POST", "/v1/summaries/query", `{}`, 200},
		{"POST", "/v1/summaries/query", `{"task_id":"task","after":"cursor","limit":1}`, 200},
		{"GET", "/v1/summaries/attempt", "", 200},
		{"POST", "/v1/summaries/reviews", httpReviewBody, 201},
		{"GET", "/v1/summaries/attempt/reviews", "", 200},
	} {
		t.Run(tc.method+tc.path+tc.body, func(t *testing.T) {
			s := httpSummaryServices()
			calls := 0
			s.Summarize = func(ctx context.Context, task, model string, keep int, cost float64) (sessions.SummaryAttempt, error) {
				calls++
				if task != "task" || model != "model" || keep != 2 || cost != .25 {
					t.Fatal(task, model, keep, cost)
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("missing deadline")
				}
				return sessions.SummaryAttempt{ID: "attempt"}, nil
			}
			s.SummaryAttempt = func(_ context.Context, id string) (sessions.SummaryAttempt, error) {
				calls++
				if id != "attempt" {
					t.Fatal(id)
				}
				return sessions.SummaryAttempt{ID: id}, nil
			}
			s.SummaryAttempts = func(_ context.Context, task, after string, limit int) ([]sessions.SummaryAttempt, error) {
				calls++
				if tc.body == `{}` {
					if task != "" || after != "" || limit != 100 {
						t.Fatal(task, after, limit)
					}
				} else if task != "task" || after != "cursor" || limit != 1 {
					t.Fatal(task, after, limit)
				}
				return []sessions.SummaryAttempt{}, nil
			}
			s.ReviewSummary = func(_ context.Context, attempt, expected, decision, note string) (sessions.SummaryReview, error) {
				calls++
				if attempt != "attempt" || expected != "previous" || decision != "approved" || note != "Checked source" {
					t.Fatal(attempt, expected, decision, note)
				}
				return sessions.SummaryReview{ID: "review"}, nil
			}
			s.SummaryReviews = func(_ context.Context, id string) ([]sessions.SummaryReview, error) {
				calls++
				if id != "attempt" {
					t.Fatal(id)
				}
				return []sessions.SummaryReview{}, nil
			}
			h, _ := New(token, 1, s)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request(tc.method, tc.path, tc.body))
			if w.Code != tc.status || calls != 1 {
				t.Fatal(w.Code, calls, w.Body.String())
			}
		})
	}
}

func TestHTTPSummaryStrictFields(t *testing.T) {
	for _, tc := range []struct{ path, body string }{
		{"/v1/summaries", `{}`}, {"/v1/summaries", `null`}, {"/v1/summaries", httpSummaryBody + `{}`},
		{"/v1/summaries", strings.Replace(httpSummaryBody, `"keep":2`, `"keep":null`, 1)},
		{"/v1/summaries", strings.Replace(httpSummaryBody, `"keep":2`, `"keep":"2"`, 1)},
		{"/v1/summaries", strings.Replace(httpSummaryBody, `"keep":2`, `"keep":1.5`, 1)},
		{"/v1/summaries", strings.Replace(httpSummaryBody, `"keep":2`, `"keep":0`, 1)},
		{"/v1/summaries", strings.Replace(httpSummaryBody, `"keep":2`, `"keep":100001`, 1)},
		{"/v1/summaries", strings.Replace(httpSummaryBody, `"max_cost":0.25`, `"max_cost":-1`, 1)},
		{"/v1/summaries", strings.Replace(httpSummaryBody, `"max_cost":0.25`, `"max_cost":null`, 1)},
		{"/v1/summaries", strings.Replace(httpSummaryBody, `"max_cost":0.25`, `"max_cost":1e999`, 1)},
		{"/v1/summaries", strings.Replace(httpSummaryBody, `"task_id":"task"`, `"task_id":"task","task_id":"other"`, 1)},
		{"/v1/summaries", strings.Replace(httpSummaryBody, `"task_id":"task"`, `"task_id":"bad/id"`, 1)},
		{"/v1/summaries", strings.Replace(httpSummaryBody, `"model_id":"model"`, `"model_id":"bad model"`, 1)},
		{"/v1/summaries", strings.Replace(httpSummaryBody, `"model_id":"model"`, `"model_id":"`+strings.Repeat("a", 129)+`"`, 1)},
		{"/v1/summaries", strings.Replace(httpSummaryBody, `"model_id":"model"`, `"Model_ID":"model"`, 1)},
		{"/v1/summaries", strings.TrimSuffix(httpSummaryBody, "}") + `,"unknown":true}`},
		{"/v1/summaries/query", `{"limit":0}`}, {"/v1/summaries/query", `{"limit":101}`}, {"/v1/summaries/query", `{"limit":null}`}, {"/v1/summaries/query", `{"limit":1,"limit":2}`},
		{"/v1/summaries/query", `{"after":"bad\n"}`},
		{"/v1/summaries/reviews", strings.Replace(httpReviewBody, `"approved"`, `"approve"`, 1)},
		{"/v1/summaries/reviews", strings.Replace(httpReviewBody, `"Checked source"`, `null`, 1)},
		{"/v1/summaries/reviews", strings.Replace(httpReviewBody, `"Checked source"`, `" "`, 1)},
		{"/v1/summaries/reviews", strings.Replace(httpReviewBody, `"Checked source"`, `"`+strings.Repeat("a", 4097)+`"`, 1)},
		{"/v1/summaries/reviews", strings.Replace(httpReviewBody, `"Checked source"`, `"`+string([]byte{255})+`"`, 1)},
	} {
		t.Run(tc.path+tc.body, func(t *testing.T) {
			s := httpSummaryServices()
			s.Summarize = func(context.Context, string, string, int, float64) (sessions.SummaryAttempt, error) {
				t.Fatal("invalid draft dispatched")
				return sessions.SummaryAttempt{}, nil
			}
			s.SummaryAttempts = func(context.Context, string, string, int) ([]sessions.SummaryAttempt, error) {
				t.Fatal("invalid query dispatched")
				return nil, nil
			}
			s.ReviewSummary = func(context.Context, string, string, string, string) (sessions.SummaryReview, error) {
				t.Fatal("invalid review dispatched")
				return sessions.SummaryReview{}, nil
			}
			h, _ := New(token, 1, s)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request("POST", tc.path, tc.body))
			if w.Code != 400 {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}

type summaryUnreadBody struct{ t *testing.T }

func (b summaryUnreadBody) Read([]byte) (int, error) {
	b.t.Fatal("capacity rejection read body")
	return 0, io.EOF
}
func (summaryUnreadBody) Close() error { return nil }

func TestHTTPSummarySharedAdmission(t *testing.T) {
	for _, path := range []string{"/v1/summaries", "/v1/summaries/query", "/v1/summaries/attempt", "/v1/summaries/reviews", "/v1/summaries/attempt/reviews"} {
		for _, mode := range []string{"auth", "origin", "query", "capacity", "unavailable"} {
			t.Run(path+mode, func(t *testing.T) {
				s := httpSummaryServices()
				if mode == "unavailable" {
					s = services()
				}
				h, _ := New(token, 1, s)
				method, body := "POST", httpSummaryBody
				if strings.Contains(path, "/attempt") {
					method, body = "GET", ""
				} else if strings.HasSuffix(path, "query") {
					body = `{}`
				} else if strings.HasSuffix(path, "reviews") {
					body = httpReviewBody
				}
				r := request(method, path, body)
				want := 503
				switch mode {
				case "auth":
					r.Header.Del("Authorization")
					want = 401
				case "origin":
					r.Header.Set("Origin", "https://example.com")
					want = 403
				case "query":
					r.URL.RawQuery = "a=b"
					want = 400
				case "capacity":
					h.slots <- struct{}{}
					r.Body = summaryUnreadBody{t}
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != want {
					t.Fatal(w.Code, w.Body.String())
				}
			})
		}
	}
	for _, mode := range []string{"media", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			h, _ := New(token, 1, httpSummaryServices())
			body := httpSummaryBody
			if mode == "oversize" {
				body += strings.Repeat(" ", 8<<10)
			}
			r := request("POST", "/v1/summaries", body)
			want := 413
			if mode == "media" {
				r.Header.Set("Content-Type", "text/plain")
				want = 415
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != want {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}

func TestHTTPSummarySafeErrorsAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
	}{{app.ErrAdmission, 422}, {sessions.ErrHistory, 422}, {telemetry.ErrConflict, 409}, {errors.New("private error"), 500}} {
		s := httpSummaryServices()
		s.Summarize = func(context.Context, string, string, int, float64) (sessions.SummaryAttempt, error) {
			return sessions.SummaryAttempt{ID: "attempt", Draft: &sessions.SummaryDraft{Model: "private draft"}}, tc.err
		}
		h, _ := New(token, 1, s)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request("POST", "/v1/summaries", httpSummaryBody))
		var body map[string]any
		if json.Unmarshal(w.Body.Bytes(), &body) != nil || w.Code != tc.status || body["summary_attempt_id"] != "attempt" || strings.Contains(w.Body.String(), "private") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/v1/summaries/attempt", "/v1/summaries/attempt/reviews"} {
		s := httpSummaryServices()
		s.SummaryAttempt = func(context.Context, string) (sessions.SummaryAttempt, error) {
			return sessions.SummaryAttempt{}, sql.ErrNoRows
		}
		s.SummaryReviews = func(context.Context, string) ([]sessions.SummaryReview, error) { return nil, sql.ErrNoRows }
		h, _ := New(token, 1, s)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request("GET", path, ""))
		if w.Code != 404 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := httpSummaryServices()
	s.Summarize = func(ctx context.Context, _ string, _ string, _ int, _ float64) (sessions.SummaryAttempt, error) {
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Fatal("cancellation lost")
		}
		return sessions.SummaryAttempt{}, ctx.Err()
	}
	h, _ := New(token, 1, s)
	h.ServeHTTP(httptest.NewRecorder(), request("POST", "/v1/summaries", httpSummaryBody).WithContext(ctx))
	if len(h.slots) != 0 {
		t.Fatal("canceled request retained slot")
	}
}

func TestHTTPSummaryInclusiveBoundsAndFirstRejection(t *testing.T) {
	s := httpSummaryServices()
	s.Summarize = func(_ context.Context, task, model string, keep int, cost float64) (sessions.SummaryAttempt, error) {
		if len(task) != 128 || len(model) != 128 || keep != 100000 || cost != 0 {
			t.Fatal(task, model, keep, cost)
		}
		return sessions.SummaryAttempt{ID: "attempt"}, nil
	}
	s.ReviewSummary = func(_ context.Context, attempt, expected, decision, note string) (sessions.SummaryReview, error) {
		if attempt != "attempt" || expected != "" || decision != "rejected" || len(note) != 4096 {
			t.Fatal(attempt, expected, decision, len(note))
		}
		return sessions.SummaryReview{ID: "review"}, nil
	}
	h, _ := New(token, 1, s)
	for _, tc := range []struct{ path, body string }{
		{"/v1/summaries", `{"task_id":"` + strings.Repeat("a", 128) + `","model_id":"` + strings.Repeat("b", 128) + `","keep":100000,"max_cost":0}`},
		{"/v1/summaries/reviews", `{"attempt_id":"attempt","decision":"rejected","note":"` + strings.Repeat("n", 4096) + `"}`},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request("POST", tc.path, tc.body))
		if w.Code != 201 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}
