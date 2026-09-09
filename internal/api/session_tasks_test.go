package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func sessionTaskPageFixture(t *testing.T) sessions.SessionTaskPage {
	t.Helper()
	cursor, err := sessions.EncodeSessionTaskCursor(sessions.SessionTaskCursor{Version: 1, SessionID: "session", Last: 2, HighWater: 3})
	if err != nil {
		t.Fatal(err)
	}
	return sessions.SessionTaskPage{Version: 1, SessionID: "session", Items: []sessions.SessionTask{{Version: 1, TaskID: "task", SessionID: "session", ParentTaskID: "parent", State: "completed", Sequence: 4, StartedAt: time.Unix(100, 0), Fence: sessions.TaskHeadFence{Version: 1, TaskID: "task", SessionID: "session", HeadSequence: 4, HeadEventID: "task-terminal"}}}, HasMore: true, NextCursor: cursor}
}

func sessionTaskListRequest(path string) *http.Request {
	r := request(http.MethodGet, path, "")
	r.Body = http.NoBody
	return r
}

func TestSessionTaskListHTTPIsStrictAndMetadataOnly(t *testing.T) {
	page := sessionTaskPageFixture(t)
	s := services()
	calls := 0
	s.SessionTasks = func(_ context.Context, session string, options sessions.SessionTaskListOptions) (sessions.SessionTaskPage, error) {
		calls++
		if session != "session" || options.Limit != 1 || options.After != "" {
			t.Fatal(session, options)
		}
		return page, nil
	}
	h, _ := New(token, 1, s)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, sessionTaskListRequest("/v1/sessions/session/tasks?limit=1"))
	var got sessions.SessionTaskPage
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Validate() != nil || got.SessionID != "session" || len(got.Items) != 1 || got.Items[0].TaskID != "task" || calls != 1 {
		t.Fatal(w.Code, w.Body.String(), calls)
	}
	for _, private := range []string{"prompt", "output", "tool_call", "endpoint", "credential"} {
		if strings.Contains(w.Body.String(), private) {
			t.Fatal("private field leaked", private)
		}
	}
	for _, path := range []string{
		"/v1/sessions/session/tasks?unknown=1",
		"/v1/sessions/session/tasks?limit=01",
		"/v1/sessions/session/tasks?limit=101",
		"/v1/sessions/bad:session/tasks",
		"/v1/sessions/session/tasks?",
	} {
		before := calls
		w = httptest.NewRecorder()
		r := sessionTaskListRequest(path)
		if strings.HasSuffix(path, "?") {
			r.URL.ForceQuery = true
		}
		h.ServeHTTP(w, r)
		if w.Code != http.StatusBadRequest || calls != before {
			t.Fatal(path, w.Code, w.Body.String(), calls)
		}
	}
}

func TestSessionTaskListHTTPControlsAndFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name    string
		page    sessions.SessionTaskPage
		panic   bool
		service bool
		busy    bool
	}{
		{name: "missing"},
		{name: "wrong session", service: true, page: sessions.SessionTaskPage{Version: 1, SessionID: "other", Items: []sessions.SessionTask{}}},
		{name: "panic", service: true, panic: true},
		{name: "capacity", service: true, page: sessionTaskPageFixture(t), busy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := services()
			if tc.service {
				s.SessionTasks = func(context.Context, string, sessions.SessionTaskListOptions) (sessions.SessionTaskPage, error) {
					if tc.panic {
						panic("private panic")
					}
					return tc.page, nil
				}
			}
			h, _ := New(token, 1, s)
			if tc.busy {
				h.controls <- struct{}{}
				h.controls <- struct{}{}
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, sessionTaskListRequest("/v1/sessions/session/tasks?limit=1"))
			if w.Code != http.StatusServiceUnavailable && w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "private") {
				t.Fatal(w.Code, w.Body.String())
			}
			if tc.busy && w.Header().Get("Retry-After") != "1" {
				t.Fatal("missing capacity retry header")
			}
		})
	}
}

func TestSessionTaskListHTTPRejectsAuthOriginAndBodies(t *testing.T) {
	s := services()
	s.SessionTasks = func(context.Context, string, sessions.SessionTaskListOptions) (sessions.SessionTaskPage, error) {
		return sessions.SessionTaskPage{Version: 1, SessionID: "session", Items: []sessions.SessionTask{}}, nil
	}
	h, _ := New(token, 1, s)
	for _, tc := range []struct {
		name string
		want int
		edit func(*http.Request)
	}{
		{name: "auth", want: http.StatusUnauthorized, edit: func(r *http.Request) { r.Header.Del("Authorization") }},
		{name: "origin", want: http.StatusForbidden, edit: func(r *http.Request) { r.Header.Set("Origin", "https://example.invalid") }},
		{name: "body", want: http.StatusBadRequest, edit: func(r *http.Request) { r.Body = http.NoBody; r.ContentLength = 1 }},
		{name: "transfer", want: http.StatusBadRequest, edit: func(r *http.Request) { r.TransferEncoding = []string{"chunked"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := sessionTaskListRequest("/v1/sessions/session/tasks")
			tc.edit(r)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}
