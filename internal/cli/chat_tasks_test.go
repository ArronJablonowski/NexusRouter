package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/sessions"
)

func TestChatTaskDiscoveryDoesNotSelectHistory(t *testing.T) {
	cursor, err := sessions.EncodeTaskListCursor(sessions.TaskListCursor{Version: 1, Last: 1, HighWater: 2})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	hooks := chatHooks{ListTasks: func(_ context.Context, options sessions.TaskListOptions) (sessions.TaskPage, error) {
		calls++
		if options.Limit != 10 || options.After != "" {
			t.Fatal(options)
		}
		return sessions.TaskPage{Version: 1, Items: []sessions.TaskSummary{{Version: 1, TaskID: "task", SessionID: "session", State: "completed", Sequence: 4, StartedAt: time.Unix(100, 0)}}, HasMore: true, NextCursor: cursor}, nil
	}}
	out := runChatTasks(context.Background(), "", hooks)
	if calls != 1 || !strings.Contains(out, "task  completed") || !strings.Contains(out, "/tasks "+cursor) || strings.Contains(out, "/resume task") {
		t.Fatal(out, calls)
	}
	if got := runChatTasks(context.Background(), "bad cursor", hooks); got != "Saved task list unavailable.\n" || calls != 1 {
		t.Fatal(got, calls)
	}
}

func TestChatTaskDiscoveryFailsClosed(t *testing.T) {
	for _, hooks := range []chatHooks{
		{},
		{ListTasks: func(context.Context, sessions.TaskListOptions) (sessions.TaskPage, error) {
			return sessions.TaskPage{}, errors.New("private")
		}},
		{ListTasks: func(context.Context, sessions.TaskListOptions) (sessions.TaskPage, error) {
			return sessions.TaskPage{Version: 1, Items: []sessions.TaskSummary{{TaskID: "private"}}}, nil
		}},
	} {
		if got := runChatTasks(context.Background(), "", hooks); got != "Saved task list unavailable.\n" || strings.Contains(got, "private") {
			t.Fatal(got)
		}
	}
}
