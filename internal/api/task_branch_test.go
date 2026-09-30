package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/sessions"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

const branchBody = `{"version":1,"source":{"version":1,"task_id":"source","session_id":"session","head_sequence":2,"head_event_id":"source-terminal"},"request":{"model_id":"model","prompt":"branch prompt"}}`

func branchStatus() submissions.Status {
	now := time.Unix(100, 0).UTC()
	return submissions.Status{Version: 1, ID: "branch-submission", State: "queued", CreatedAt: now, UpdatedAt: now, ConfigDigest: strings.Repeat("a", 64)}
}

func branchRequest(body string) *http.Request {
	r := request(http.MethodPost, "/v1/tasks/source/branches", body)
	r.Header.Set("Idempotency-Key", "fixture-branch-key")
	return r
}

func TestTaskBranchStrictAdmissionAndBinding(t *testing.T) {
	var calls atomic.Int32
	s := services()
	s.SubmitBranch = func(_ context.Context, key string, source sessions.TaskHeadFence, request app.Request) (submissions.Status, error) {
		calls.Add(1)
		if key != "fixture-branch-key" || source.TaskID != "source" || source.SessionID != "session" || source.HeadSequence != 2 || source.HeadEventID != "source-terminal" || request.ModelID != "model" || request.Prompt != "branch prompt" || request.ContinueTaskID != "" {
			t.Fatal("adapter changed branch admission", key, source, request)
		}
		return branchStatus(), nil
	}
	h, err := New(token, 1, s)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, branchRequest(branchBody))
	if w.Code != http.StatusAccepted || calls.Load() != 1 || !strings.Contains(w.Body.String(), `"id":"branch-submission"`) {
		t.Fatal(w.Code, calls.Load(), w.Body.String())
	}

	invalid := []string{
		`{}`,
		strings.Replace(branchBody, `"version":1`, `"version":2`, 1),
		strings.Replace(branchBody, `"version":1`, `"version":1,"version":1`, 1),
		strings.Replace(branchBody, `"source":`, `"unknown":true,"source":`, 1),
		strings.Replace(branchBody, `"head_event_id":"source-terminal"`, `"head_event_id":"source-terminal","head_event_id":"other"`, 1),
		strings.Replace(branchBody, `"task_id":"source"`, `"task_id":"other"`, 1),
		strings.Replace(branchBody, `"head_sequence":2`, `"head_sequence":2.0`, 1),
		strings.Replace(branchBody, `"prompt":"branch prompt"`, `"prompt":"branch prompt","prompt":"other"`, 1),
		strings.Replace(branchBody, `"prompt":"branch prompt"`, `"prompt":"branch prompt","continue_task_id":"source"`, 1),
		strings.TrimSuffix(branchBody, "}") + `,"request":{}}`,
		branchBody + `{}`,
	}
	for i, body := range invalid {
		before := calls.Load()
		w = httptest.NewRecorder()
		h.ServeHTTP(w, branchRequest(body))
		if w.Code != http.StatusBadRequest || calls.Load() != before {
			t.Fatalf("invalid case %d dispatched: status=%d calls=%d body=%s", i, w.Code, calls.Load(), w.Body.String())
		}
	}
}

func TestTaskBranchTransportGuardsAndSanitizedFailures(t *testing.T) {
	for _, mode := range []string{"auth", "origin", "query", "method", "key", "media", "missing", "conflict", "admission", "panic"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			s := services()
			s.SubmitBranch = func(context.Context, string, sessions.TaskHeadFence, app.Request) (submissions.Status, error) {
				calls.Add(1)
				switch mode {
				case "conflict":
					return submissions.Status{}, submissions.ErrConflict
				case "admission":
					return submissions.Status{}, app.ErrAdmission
				case "panic":
					panic("private branch secret")
				}
				return branchStatus(), nil
			}
			if mode == "missing" {
				s.SubmitBranch = nil
			}
			h, err := New(token, 1, s)
			if err != nil {
				t.Fatal(err)
			}
			r := branchRequest(branchBody)
			want := http.StatusAccepted
			switch mode {
			case "auth":
				r.Header.Del("Authorization")
				want = http.StatusUnauthorized
			case "origin":
				r.Header.Set("Origin", "https://example.com")
				want = http.StatusForbidden
			case "query":
				r.URL.RawQuery = "secret=value"
				want = http.StatusBadRequest
			case "method":
				r.Method = http.MethodPut
				want = http.StatusNotFound
			case "key":
				r.Header.Del("Idempotency-Key")
				want = http.StatusBadRequest
			case "media":
				r.Header.Set("Content-Type", "text/plain")
				want = http.StatusUnsupportedMediaType
			case "missing":
				want = http.StatusServiceUnavailable
			case "conflict":
				want = http.StatusConflict
			case "admission":
				want = http.StatusUnprocessableEntity
			case "panic":
				want = http.StatusInternalServerError
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != want || strings.Contains(w.Body.String(), "private") {
				t.Fatal(w.Code, w.Body.String())
			}
			if want < 500 && mode != "conflict" && mode != "admission" && calls.Load() != 0 {
				t.Fatal("transport rejection dispatched", calls.Load())
			}
		})
	}
}

func TestTaskBranchRejectsOversizeBeforeDispatch(t *testing.T) {
	var calls atomic.Int32
	s := services()
	s.SubmitBranch = func(context.Context, string, sessions.TaskHeadFence, app.Request) (submissions.Status, error) {
		calls.Add(1)
		return submissions.Status{}, errors.New("must not run")
	}
	h, _ := New(token, 1, s)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, branchRequest(branchBody+strings.Repeat(" ", 1<<20)))
	if w.Code != http.StatusRequestEntityTooLarge || calls.Load() != 0 {
		t.Fatal(w.Code, calls.Load(), w.Body.String())
	}
}
