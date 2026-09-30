package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

const submissionFixtureKey = "private-intake-key-12345"

func submissionFixtureStatus(state string) submissions.Status {
	s := submissions.Status{Version: 1, ID: "submission", State: state, CreatedAt: time.Unix(100, 0).UTC(), UpdatedAt: time.Unix(100, 0).UTC(), ConfigDigest: strings.Repeat("a", 64), TaskIDs: []string{}}
	switch state {
	case "running":
		at := time.Unix(200, 0).UTC()
		s.LeaseExpiresAt = &at
	case "succeeded":
		s.TaskIDs = []string{"task"}
		s.Result = &submissions.Result{TaskID: "task", Text: "answer", Turns: 1, FinishReason: "stop"}
	case "failed":
		s.ErrorCode = "execution_failed"
	case "canceled":
		s.CancelRequested = true
	}
	return s
}

func submissionRequest(method, path, body string) *http.Request {
	r := request(method, path, body)
	r.Header.Set("Idempotency-Key", submissionFixtureKey)
	return r
}

func TestSubmissionRoutesOnlyCallDetachedServices(t *testing.T) {
	for _, state := range []string{"queued", "running", "succeeded", "failed", "canceled"} {
		for _, operation := range []string{"submit", "status", "cancel"} {
			s := services()
			s.Run = func(context.Context, app.Request) (app.Result, error) {
				t.Error("detached intake executed synchronously")
				return app.Result{}, nil
			}
			s.RunStream = func(context.Context, app.Request, func(runtime.Event) error) (app.Result, error) {
				t.Error("detached intake executed streaming task")
				return app.Result{}, nil
			}
			status := submissionFixtureStatus(state)
			calls := map[string]int{}
			s.Submit = func(_ context.Context, key string, r app.Request) (submissions.Status, error) {
				calls["submit"]++
				if key != submissionFixtureKey || r.ModelID != "m" || r.Prompt != "private-input-prompt" {
					t.Error(key, r)
				}
				return status, nil
			}
			s.Submission = func(_ context.Context, id string) (submissions.Status, error) {
				calls["status"]++
				if id != "submission" {
					t.Error(id)
				}
				return status, nil
			}
			s.CancelSubmission = func(_ context.Context, id string) (submissions.Status, error) {
				calls["cancel"]++
				if id != "submission" {
					t.Error(id)
				}
				return status, nil
			}
			h, err := New(token, 1, s)
			if err != nil {
				t.Fatal(err)
			}
			method, path, body := "POST", "/v1/submissions", `{"model_id":"m","prompt":"private-input-prompt"}`
			if operation == "status" {
				method, path, body = "GET", "/v1/submissions/submission", ""
			}
			if operation == "cancel" {
				path, body = "/v1/submissions/submission/cancel", `{}`
			}
			want := 200
			if operation != "status" && (state == "queued" || state == "running") {
				want = 202
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, submissionRequest(method, path, body))
			var got submissions.Status
			if w.Code != want || json.Unmarshal(w.Body.Bytes(), &got) != nil || !reflect.DeepEqual(got, status) || calls[operation] != 1 || len(calls) != 1 {
				t.Fatal(operation, state, w.Code, w.Body.String(), calls)
			}
			for _, secret := range []string{submissionFixtureKey, "private-input-prompt", token} {
				if strings.Contains(w.Body.String(), secret) {
					t.Fatal("intake secret echoed", w.Body.String())
				}
			}
			var raw map[string]json.RawMessage
			json.Unmarshal(w.Body.Bytes(), &raw)
			for _, field := range []string{"request", "token", "key", "idempotency_key"} {
				if raw[field] != nil {
					t.Fatal("private envelope field serialized", field)
				}
			}
		}
	}
}

func TestSubmissionStrictIdempotencyHeaders(t *testing.T) {
	for _, values := range [][]string{nil, {}, {""}, {"short"}, {strings.Repeat("x", 129)}, {"contains a space!"}, {"newline-secret\n!!"}, {"non-ascii-secret-é"}, {submissionFixtureKey, submissionFixtureKey}} {
		s := services()
		s.Submit = func(context.Context, string, app.Request) (submissions.Status, error) {
			t.Error("invalid key admitted")
			return submissionFixtureStatus("queued"), nil
		}
		h, _ := New(token, 1, s)
		r := request("POST", "/v1/submissions", streamBody)
		if values != nil {
			r.Header["Idempotency-Key"] = values
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatal(values, w.Code, w.Body.String())
		}
	}
	s := services()
	s.Submit = func(context.Context, string, app.Request) (submissions.Status, error) {
		t.Error("case duplicate admitted")
		return submissionFixtureStatus("queued"), nil
	}
	h, _ := New(token, 1, s)
	r := submissionRequest("POST", "/v1/submissions", streamBody)
	r.Header["idempotency-key"] = []string{submissionFixtureKey}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, n := range []int{16, 128} {
		calls := 0
		s := services()
		s.Submit = func(_ context.Context, key string, _ app.Request) (submissions.Status, error) {
			calls++
			if len(key) != n {
				t.Error(len(key))
			}
			return submissionFixtureStatus("queued"), nil
		}
		h, _ := New(token, 1, s)
		r := submissionRequest("POST", "/v1/submissions", streamBody)
		r.Header.Set("Idempotency-Key", strings.Repeat("x", n))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 202 || calls != 1 {
			t.Fatal(n, w.Code, calls)
		}
	}
}

func TestSubmissionStrictBodiesAndRoutes(t *testing.T) {
	for _, operation := range []string{"submit", "cancel"} {
		bodies := []string{"", `null`, `[]`, `{} {}`, `{"unexpected":"private-body"}`, `{"a":1,"a":2}`}
		if operation == "submit" {
			bodies = append(bodies, `{}`, `{"model_id":"m","prompt":null}`, `{"model_id":"m","model_id":"m","prompt":"x"}`, `{"model_id":"m","prompt":"`+strings.Repeat("x", 1<<20)+`"}`)
		} else {
			bodies = append(bodies, "{}"+strings.Repeat(" ", 1023))
		}
		for _, body := range bodies {
			s := services()
			s.Submit = func(context.Context, string, app.Request) (submissions.Status, error) {
				t.Error("invalid body submitted")
				return submissions.Status{}, nil
			}
			s.CancelSubmission = func(context.Context, string) (submissions.Status, error) {
				t.Error("invalid cancel dispatched")
				return submissions.Status{}, nil
			}
			h, _ := New(token, 1, s)
			path := "/v1/submissions"
			if operation == "cancel" {
				path += "/submission/cancel"
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, submissionRequest("POST", path, body))
			want := 400
			if operation == "cancel" && len(body) > 1024 {
				want = 413
			}
			if w.Code != want || strings.Contains(w.Body.String(), "private-body") {
				t.Fatal(operation, w.Code, w.Body.String())
			}
		}
	}
	for _, id := range []string{"", "a/b", "a:b", "a b", "a\n", strings.Repeat("x", 129)} {
		for _, cancel := range []bool{false, true} {
			s := services()
			s.Submission = func(context.Context, string) (submissions.Status, error) {
				t.Error("invalid ID read")
				return submissions.Status{}, nil
			}
			s.CancelSubmission = s.Submission
			h, _ := New(token, 1, s)
			method, path := "GET", "/v1/submissions/"+id
			if cancel {
				method, path = "POST", path+"/cancel"
			}
			r := submissionRequest(method, "/v1/submissions/submission", `{}`)
			r.URL.Path = path
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 400 {
				t.Fatal(method, path, w.Code, w.Body.String())
			}
		}
	}
}

func TestSubmissionAuthenticationMediaAndMissingHooks(t *testing.T) {
	for _, operation := range []string{"submit", "status", "cancel"} {
		for _, condition := range []string{"auth", "origin", "query", "missing", "media"} {
			if operation == "status" && condition == "media" {
				continue
			}
			s := services()
			calls := 0
			s.Submit = func(context.Context, string, app.Request) (submissions.Status, error) {
				calls++
				return submissionFixtureStatus("queued"), nil
			}
			s.Submission = func(context.Context, string) (submissions.Status, error) {
				calls++
				return submissionFixtureStatus("queued"), nil
			}
			s.CancelSubmission = s.Submission
			if condition == "missing" {
				s.Submit, s.Submission, s.CancelSubmission = nil, nil, nil
			}
			h, _ := New(token, 1, s)
			method, path, body := "POST", "/v1/submissions", streamBody
			if operation == "status" {
				method, path, body = "GET", path+"/submission", ""
			}
			if operation == "cancel" {
				path, body = path+"/submission/cancel", `{}`
			}
			r := submissionRequest(method, path, body)
			want := 503
			switch condition {
			case "auth":
				r.Header.Del("Authorization")
				want = 401
			case "origin":
				r.Header.Set("Origin", "https://example.com")
				want = 403
			case "query":
				r.URL.RawQuery = "secret=private-query"
				want = 400
			case "media":
				r.Header.Set("Content-Type", "text/plain")
				want = 415
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != want || calls != 0 || strings.Contains(w.Body.String(), "private-query") {
				t.Fatal(operation, condition, w.Code, calls, w.Body.String())
			}
		}
	}
}

func TestSubmissionErrorMappingAndInvalidStatus(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code int
	}{{sql.ErrNoRows, 404}, {submissions.ErrConflict, 409}, {submissions.ErrCapacity, 503}, {submissions.ErrInvalid, 400}, {app.ErrAdmission, 422}, {errors.New("private-error"), 500}} {
		for _, operation := range []string{"submit", "status", "cancel"} {
			s := services()
			s.Submit = func(context.Context, string, app.Request) (submissions.Status, error) {
				return submissions.Status{}, tc.err
			}
			s.Submission = func(context.Context, string) (submissions.Status, error) { return submissions.Status{}, tc.err }
			s.CancelSubmission = s.Submission
			h, _ := New(token, 1, s)
			method, path, body := "POST", "/v1/submissions", streamBody
			if operation == "status" {
				method, path, body = "GET", path+"/submission", ""
			}
			if operation == "cancel" {
				path, body = path+"/submission/cancel", `{}`
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, submissionRequest(method, path, body))
			if w.Code != tc.code || strings.Contains(w.Body.String(), "private-error") || tc.code == 503 && w.Header().Get("Retry-After") != "1" {
				t.Fatal(operation, w.Code, w.Body.String())
			}
		}
	}
	for _, mutate := range []func(*submissions.Status){func(s *submissions.Status) { s.Version = 0 }, func(s *submissions.Status) { s.State = "unknown" }, func(s *submissions.Status) { s.ID = "other" }, func(s *submissions.Status) { s.ConfigDigest = "private-digest" }, func(s *submissions.Status) { s.ErrorCode = "private-error" }, func(s *submissions.Status) { s.Result = &submissions.Result{Text: "private-result"} }} {
		status := submissionFixtureStatus("queued")
		mutate(&status)
		s := services()
		s.Submission = func(context.Context, string) (submissions.Status, error) { return status, nil }
		h, _ := New(token, 1, s)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request("GET", "/v1/submissions/submission", ""))
		if w.Code != 500 || strings.Contains(w.Body.String(), "private-") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}

func TestSubmissionCapacityIsSeparateAndBoundedBeforeBodies(t *testing.T) {
	s := services()
	calls := 0
	s.Submit = func(context.Context, string, app.Request) (submissions.Status, error) {
		calls++
		return submissionFixtureStatus("queued"), nil
	}
	s.Submission = func(context.Context, string) (submissions.Status, error) {
		calls++
		return submissionFixtureStatus("queued"), nil
	}
	s.CancelSubmission = s.Submission
	h, _ := New(token, 1, s)
	h.slots <- struct{}{}
	defer func() { <-h.slots }()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, submissionRequest("POST", "/v1/submissions", streamBody))
	if w.Code != 202 || calls != 1 {
		t.Fatal("execution capacity blocked intake", w.Code, calls)
	}
	h.intake <- struct{}{}
	h.intake <- struct{}{}
	defer func() { <-h.intake; <-h.intake }()
	body := &streamUnreadBody{}
	r := submissionRequest("POST", "/v1/submissions", "")
	r.Body = body
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 503 || body.reads != 0 || calls != 1 || w.Header().Get("Retry-After") != "1" {
		t.Fatal("full intake read body/dispatched", w.Code, body.reads, calls)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, request("GET", "/v1/submissions/submission", ""))
	if w.Code != 200 || calls != 2 {
		t.Fatal("intake capacity blocked controls", w.Code, calls)
	}
	h.controls <- struct{}{}
	h.controls <- struct{}{}
	defer func() { <-h.controls; <-h.controls }()
	body = &streamUnreadBody{}
	r = request("POST", "/v1/submissions/submission/cancel", "")
	r.Body = body
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 503 || body.reads != 0 || calls != 2 {
		t.Fatal("full control capacity read body/dispatched", w.Code, body.reads, calls)
	}
}

func TestSubmissionTerminalFailureRetainsSafeAttribution(t *testing.T) {
	for _, state := range []string{"failed", "canceled"} {
		status := submissionFixtureStatus(state)
		status.TaskIDs = []string{"prior", "task"}
		status.ErrorCode = "canceled"
		status.Result = &submissions.Result{TaskID: "task", PreviousTaskIDs: []string{"prior"}, Turns: 0}
		s := services()
		s.Submission = func(context.Context, string) (submissions.Status, error) { return status, nil }
		h, _ := New(token, 1, s)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request("GET", "/v1/submissions/submission", ""))
		var got submissions.Status
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || !reflect.DeepEqual(got, status) {
			t.Fatal("valid failure attribution rejected", w.Code, w.Body.String())
		}
		status.Result.Text = "private-partial-output"
		w = httptest.NewRecorder()
		h.ServeHTTP(w, request("GET", "/v1/submissions/submission", ""))
		if w.Code != 500 || strings.Contains(w.Body.String(), "private-partial-output") {
			t.Fatal("failed partial output exposed", w.Code, w.Body.String())
		}
	}
}

func TestSubmissionRoutesRejectUnsupportedMethods(t *testing.T) {
	s := services()
	s.Submit = func(context.Context, string, app.Request) (submissions.Status, error) {
		t.Error("unsupported route submitted")
		return submissions.Status{}, nil
	}
	s.Submission = func(context.Context, string) (submissions.Status, error) {
		t.Error("unsupported route reached hook")
		return submissions.Status{}, nil
	}
	s.CancelSubmission = s.Submission
	h, _ := New(token, 1, s)
	for _, route := range [][2]string{{"PUT", "/v1/submissions"}, {"DELETE", "/v1/submissions/submission"}, {"POST", "/v1/submissions/submission"}, {"PUT", "/v1/submissions/submission/cancel"}} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, submissionRequest(route[0], route[1], `{}`))
		if w.Code != 404 {
			t.Fatal(route, w.Code, w.Body.String())
		}
	}
}
