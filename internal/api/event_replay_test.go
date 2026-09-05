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

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func replayEventPage(after int64) sessions.EventPage {
	events := []runtime.Event{streamEvent(1, runtime.TaskStarted), streamEvent(2, runtime.TaskCompleted)}
	return sessions.EventPage{Version: 1, TaskID: "task", SessionID: "session", State: "completed", FromSequence: after, NextSequence: 2, HeadSequence: 2, Events: events[after:]}
}

func TestEventReplayExactFramesAndCheckpointWithoutExecution(t *testing.T) {
	for _, cursor := range []string{"", "task:0", "task:1", "task:2"} {
		t.Run(cursor, func(t *testing.T) {
			s := services()
			s.Run = func(context.Context, app.Request) (app.Result, error) {
				t.Error("replay executed task")
				return app.Result{}, nil
			}
			s.RunStream = func(context.Context, app.Request, func(runtime.Event) error) (app.Result, error) {
				t.Error("replay executed streaming task")
				return app.Result{}, nil
			}
			s.Inspect = func(context.Context, string) (sessions.Snapshot, error) {
				t.Error("replay used task execution/inspection instead of events")
				return sessions.Snapshot{}, nil
			}
			after := int64(0)
			if cursor == "task:1" {
				after = 1
			}
			if cursor == "task:2" {
				after = 2
			}
			page := replayEventPage(after)
			calls := 0
			s.Events = func(_ context.Context, task string, gotAfter int64, limit int) (sessions.EventPage, error) {
				calls++
				if task != "task" || gotAfter != after || limit != 100 {
					t.Error("incorrect replay query", task, gotAfter, limit)
				}
				return page, nil
			}
			h, err := New(token, 1, s)
			if err != nil {
				t.Fatal(err)
			}
			r := request("GET", "/v1/tasks/task/events", "")
			if cursor != "" {
				r.Header.Set("Last-Event-ID", cursor)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 200 || calls != 1 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") {
				t.Fatal(w.Code, calls, w.Body.String())
			}
			frames := parseStreamFrames(t, w.Body.String())
			if len(frames) != len(page.Events)+1 {
				t.Fatal(frames)
			}
			for i, event := range page.Events {
				var got runtime.Event
				if json.Unmarshal([]byte(frames[i].data), &got) != nil || !reflect.DeepEqual(got, event) || frames[i].event != string(event.Kind) || frames[i].id != []string{"", "task:1", "task:2"}[event.Sequence] {
					t.Fatal("replay changed durable event", frames[i], event)
				}
			}
			last := frames[len(frames)-1]
			var checkpoint struct {
				Version   int    `json:"version"`
				TaskID    string `json:"task_id"`
				SessionID string `json:"session_id"`
				State     string `json:"state"`
				From      int64  `json:"from_sequence"`
				Next      int64  `json:"next_sequence"`
				Head      int64  `json:"head_sequence"`
				HasMore   bool   `json:"has_more"`
			}
			if json.Unmarshal([]byte(last.data), &checkpoint) != nil || last.event != "checkpoint" || last.id != "" || checkpoint.Version != 1 || checkpoint.TaskID != "task" || checkpoint.SessionID != "session" || checkpoint.State != "completed" || checkpoint.From != after || checkpoint.Next != 2 || checkpoint.Head != 2 || checkpoint.HasMore {
				t.Fatal("checkpoint mismatch", last, checkpoint)
			}
			var fields map[string]json.RawMessage
			json.Unmarshal([]byte(last.data), &fields)
			if _, ok := fields["events"]; ok {
				t.Fatal("checkpoint repeated events")
			}
			if len(h.slots) != 0 {
				t.Fatal("replay leaked capacity")
			}
		})
	}
}

func TestEventReplayAdmission(t *testing.T) {
	for _, condition := range []string{"auth", "origin", "query", "capacity", "missing-hook"} {
		t.Run(condition, func(t *testing.T) {
			s := services()
			calls := 0
			s.Events = func(context.Context, string, int64, int) (sessions.EventPage, error) {
				calls++
				return replayEventPage(0), nil
			}
			if condition == "missing-hook" {
				s.Events = nil
			}
			h, err := New(token, 1, s)
			if err != nil {
				t.Fatal(err)
			}
			r := request("GET", "/v1/tasks/task/events", "")
			want := 503
			switch condition {
			case "auth":
				r.Header.Del("Authorization")
				want = 401
			case "origin":
				r.Header.Set("Origin", "https://example.com")
				want = 403
			case "query":
				r.URL.RawQuery = "after=1&private=query-secret"
				want = 400
			case "capacity":
				h.slots <- struct{}{}
				defer func() { <-h.slots }()
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != want || calls != 0 || !json.Valid(w.Body.Bytes()) || strings.Contains(w.Body.String(), "query-secret") {
				t.Fatal(w.Code, calls, w.Body.String())
			}
		})
	}
}

func TestEventReplayRejectsMalformedAndAmbiguousCursors(t *testing.T) {
	for _, values := range [][]string{
		{""}, {"task"}, {"task:"}, {"other:1"}, {"task:-1"}, {"task:+1"}, {"task:01"}, {"task:1.0"}, {"task:1e0"},
		{" task:1"}, {"task:1 "}, {"task:1:2"}, {"task:9223372036854775808"}, {"task:1", "task:1"}, {"task:1,task:2"}, {"task:\xff"}, {},
	} {
		s := services()
		calls := 0
		s.Events = func(context.Context, string, int64, int) (sessions.EventPage, error) {
			calls++
			return replayEventPage(0), nil
		}
		h, err := New(token, 1, s)
		if err != nil {
			t.Fatal(err)
		}
		r := request("GET", "/v1/tasks/task/events", "")
		r.Header["Last-Event-Id"] = values
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 400 || calls != 0 {
			t.Fatal(values, w.Code, calls, w.Body.String())
		}
	}
	for _, path := range []string{"/v1/tasks//events", "/v1/tasks/task:extra/events", "/v1/tasks/task/extra/events", "/v1/tasks/task space/events", "/v1/tasks/task\nextra/events", "/v1/tasks/task\xff/events", "/v1/tasks/" + strings.Repeat("x", 129) + "/events"} {
		s := services()
		s.Events = func(context.Context, string, int64, int) (sessions.EventPage, error) {
			t.Error("invalid task ID reached hook")
			return sessions.EventPage{}, nil
		}
		h, _ := New(token, 1, s)
		r := request("GET", "/v1/tasks/task/events", "")
		r.URL.Path = path
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	s := services()
	s.Events = func(context.Context, string, int64, int) (sessions.EventPage, error) {
		t.Error("case duplicate reached hook")
		return sessions.EventPage{}, nil
	}
	h, _ := New(token, 1, s)
	r := request("GET", "/v1/tasks/task/events", "")
	r.Header["Last-Event-Id"], r.Header["last-event-id"] = []string{"task:1"}, []string{"task:1"}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestEventReplayErrorsAndInvalidPagesDoNotStartSSE(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
	}{{sql.ErrNoRows, 404}, {sessions.ErrEventCursor, 409}, {sessions.ErrEventTooLarge, 413}, {errors.New("private-store-detail"), 500}} {
		s := services()
		s.Events = func(context.Context, string, int64, int) (sessions.EventPage, error) {
			return sessions.EventPage{}, tc.err
		}
		h, _ := New(token, 1, s)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request("GET", "/v1/tasks/task/events", ""))
		if w.Code != tc.status || strings.Contains(w.Body.String(), "private-store-detail") || strings.Contains(w.Body.String(), "event:") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	for name, mutate := range map[string]func(*sessions.EventPage){
		"version":            func(p *sessions.EventPage) { p.Version = 2 },
		"task":               func(p *sessions.EventPage) { p.TaskID = "other" },
		"session":            func(p *sessions.EventPage) { p.SessionID = "" },
		"state":              func(p *sessions.EventPage) { p.State = "unknown" },
		"from":               func(p *sessions.EventPage) { p.FromSequence = 1 },
		"next":               func(p *sessions.EventPage) { p.NextSequence = 1 },
		"head":               func(p *sessions.EventPage) { p.HeadSequence = 1 },
		"more":               func(p *sessions.EventPage) { p.HasMore = true },
		"missing-events":     func(p *sessions.EventPage) { p.Events = nil },
		"duplicate-sequence": func(p *sessions.EventPage) { p.Events[1].Sequence = 1 },
		"event-task":         func(p *sessions.EventPage) { p.Events[0].TaskID = "other" },
		"event-session":      func(p *sessions.EventPage) { p.Events[0].SessionID = "other" },
		"invalid-event":      func(p *sessions.EventPage) { p.Events[1].ID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			page := replayEventPage(0)
			page.Events[0].Data.Text = "private-invalid-page-content"
			mutate(&page)
			s := services()
			s.Events = func(context.Context, string, int64, int) (sessions.EventPage, error) { return page, nil }
			h, _ := New(token, 1, s)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request("GET", "/v1/tasks/task/events", ""))
			if w.Code != 500 || w.Flushed || strings.Contains(w.Header().Get("Content-Type"), "text/event-stream") || strings.Contains(w.Body.String(), "private-invalid-page-content") {
				t.Fatal("invalid page partly streamed", w.Code, w.Body.String())
			}
		})
	}
}

func TestEventReplayPartialPageCheckpointDoesNotFetchAgain(t *testing.T) {
	page := replayEventPage(0)
	page.HeadSequence, page.HasMore = 3, true
	page.Events[1] = streamEvent(2, runtime.TurnStarted)
	page.Events[1].TurnID = "turn"
	if err := page.Validate(); err != nil {
		t.Fatal("invalid page fixture", err)
	}
	s := services()
	calls := 0
	s.Events = func(context.Context, string, int64, int) (sessions.EventPage, error) { calls++; return page, nil }
	h, err := New(token, 1, s)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request("GET", "/v1/tasks/task/events", ""))
	if w.Code != 200 || calls != 1 {
		t.Fatal(w.Code, calls, w.Body.String())
	}
	frames := parseStreamFrames(t, w.Body.String())
	var checkpoint sessions.EventPage
	if len(frames) != 3 || json.Unmarshal([]byte(frames[2].data), &checkpoint) != nil || frames[2].event != "checkpoint" || frames[2].id != "" || checkpoint.NextSequence != 2 || checkpoint.HeadSequence != 3 || !checkpoint.HasMore || checkpoint.Events != nil {
		t.Fatal("invalid bounded checkpoint", frames, checkpoint)
	}
}

func TestEventReplaySinkFailureStopsWithoutExecutionOrJSONAppend(t *testing.T) {
	for _, panics := range []bool{false, true} {
		s := services()
		calls := 0
		s.Events = func(context.Context, string, int64, int) (sessions.EventPage, error) {
			calls++
			return replayEventPage(0), nil
		}
		s.Run = func(context.Context, app.Request) (app.Result, error) {
			t.Error("sink failure executed task")
			return app.Result{}, nil
		}
		h, _ := New(token, 1, s)
		w := &failingStreamWriter{header: http.Header{}, panicWrite: panics}
		h.ServeHTTP(w, request("GET", "/v1/tasks/task/events", ""))
		if calls != 1 || w.writes != 1 || len(h.slots) != 0 {
			t.Fatal("sink retried or leaked slot", panics, calls, w.writes)
		}
		for _, b := range w.attempted {
			if strings.HasPrefix(strings.TrimSpace(string(b)), "{") || strings.Contains(string(b), "private-writer-detail") {
				t.Fatal("plain JSON or raw panic appended", string(b))
			}
		}
	}
}
