package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/sessions"
)

func taskPageFixture(t *testing.T) sessions.TaskPage {
	t.Helper()
	cursor, err := sessions.EncodeTaskListCursor(sessions.TaskListCursor{Version: 1, Last: 2, HighWater: 3})
	if err != nil {
		t.Fatal(err)
	}
	return sessions.TaskPage{Version: 1, Items: []sessions.TaskSummary{{Version: 1, TaskID: "task", SessionID: "session", State: "completed", Sequence: 4, StartedAt: time.Unix(100, 0)}}, HasMore: true, NextCursor: cursor}
}

func taskListRequest(path string) *http.Request {
	r := request(http.MethodGet, path, "")
	r.Body = http.NoBody
	return r
}

func TestTaskListHTTPMetadataOnly(t *testing.T) {
	page := taskPageFixture(t)
	s := services()
	calls := 0
	s.Tasks = func(_ context.Context, options sessions.TaskListOptions) (sessions.TaskPage, error) {
		calls++
		if options.State != "" || options.Limit != 1 || options.After != "" {
			t.Fatal(options)
		}
		return page, nil
	}
	h, _ := New(token, 1, s)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, taskListRequest("/v1/tasks?limit=1"))
	var got sessions.TaskPage
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Validate() != nil || len(got.Items) != 1 || got.Items[0].TaskID != "task" || calls != 1 {
		t.Fatal(w.Code, w.Body.String(), calls)
	}
	for _, private := range []string{"prompt", "output", "tool_call", "endpoint", "credential"} {
		if strings.Contains(w.Body.String(), private) {
			t.Fatal("private field leaked", private)
		}
	}
	for _, path := range []string{"/v1/tasks?unknown=1", "/v1/tasks?limit=01", "/v1/tasks?limit=101", "/v1/tasks?state=queued", "/v1/tasks?"} {
		before := calls
		w = httptest.NewRecorder()
		r := taskListRequest(path)
		if strings.HasSuffix(path, "?") {
			r.URL.ForceQuery = true
		}
		h.ServeHTTP(w, r)
		if w.Code != http.StatusBadRequest || calls != before {
			t.Fatal(path, w.Code, w.Body.String(), calls)
		}
	}
}

func TestTaskListHTTPFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name    string
		page    sessions.TaskPage
		panic   bool
		service bool
	}{
		{name: "missing"},
		{name: "invalid", service: true, page: sessions.TaskPage{Version: 1, Items: []sessions.TaskSummary{{TaskID: "private"}}}},
		{name: "too many", service: true, page: taskPageFixture(t)},
		{name: "panic", service: true, panic: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "too many" {
				second := tc.page.Items[0]
				second.TaskID, second.SessionID = "other", "other-session"
				tc.page.Items = append(tc.page.Items, second)
			}
			s := services()
			if tc.service {
				s.Tasks = func(context.Context, sessions.TaskListOptions) (sessions.TaskPage, error) {
					if tc.panic {
						panic("private panic")
					}
					return tc.page, nil
				}
			}
			h, _ := New(token, 1, s)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, taskListRequest("/v1/tasks?limit=1"))
			if w.Code != http.StatusServiceUnavailable && w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "private") {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}
