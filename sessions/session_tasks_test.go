package sessions

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestSessionTaskCursorCanonicalAndSessionBound(t *testing.T) {
	cursor := SessionTaskCursor{Version: 1, SessionID: "session", Last: 2, HighWater: 4}
	encoded, err := EncodeSessionTaskCursor(cursor)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeSessionTaskCursor(encoded)
	if err != nil || decoded != cursor {
		t.Fatal(decoded, err)
	}
	if (SessionTaskListOptions{After: encoded, Limit: 1}).Validate("session") != nil || (SessionTaskListOptions{After: encoded, Limit: 1}).Validate("other") == nil {
		t.Fatal("cursor was not bound to its session")
	}
	for _, invalid := range []string{"", "%%%", encoded + "=", base64.RawURLEncoding.EncodeToString([]byte(`{"version":1,"session_id":"session","last":4,"high_water":2}`))} {
		if _, err := DecodeSessionTaskCursor(invalid); err == nil {
			t.Fatal("invalid cursor admitted", invalid)
		}
	}
}

func TestSessionTaskPageValidationAndContentFreeShape(t *testing.T) {
	cursor, _ := EncodeSessionTaskCursor(SessionTaskCursor{Version: 1, SessionID: "session", Last: 1, HighWater: 2})
	item := SessionTask{Version: 1, TaskID: "child", SessionID: "session", ParentTaskID: "root", RetryOfTaskID: "prior", State: "completed", Sequence: 4, StartedAt: time.Unix(100, 0).UTC(), Fence: TaskHeadFence{Version: 1, TaskID: "child", SessionID: "session", HeadSequence: 4, HeadEventID: "child-head"}}
	page := SessionTaskPage{Version: 1, SessionID: "session", Items: []SessionTask{item}, NextCursor: cursor, HasMore: true}
	if err := page.Validate(); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"messages", "prompt", "output", "tool_calls", "endpoint", "credential"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatal("content field escaped", string(body))
		}
	}
	invalid := []SessionTaskPage{
		{},
		{Version: 1, SessionID: "session", Items: []SessionTask{item, item}},
		{Version: 1, SessionID: "other", Items: []SessionTask{item}, NextCursor: cursor, HasMore: true},
	}
	badItem := item
	badItem.ParentTaskID = badItem.TaskID
	invalid = append(invalid, SessionTaskPage{Version: 1, SessionID: "session", Items: []SessionTask{badItem}})
	wrongSession := item
	wrongSession.SessionID = "other"
	invalid = append(invalid, SessionTaskPage{Version: 1, SessionID: "session", Items: []SessionTask{wrongSession}})
	wrongFence := item
	wrongFence.Fence.HeadEventID = ""
	invalid = append(invalid, SessionTaskPage{Version: 1, SessionID: "session", Items: []SessionTask{wrongFence}})
	wrongFence = item
	wrongFence.Fence.HeadSequence--
	invalid = append(invalid, SessionTaskPage{Version: 1, SessionID: "session", Items: []SessionTask{wrongFence}})
	for _, candidate := range invalid {
		if candidate.Validate() == nil {
			t.Fatal("invalid page admitted", candidate)
		}
	}
}

func TestTaskHeadFenceValidation(t *testing.T) {
	valid := TaskHeadFence{Version: 1, TaskID: "task", SessionID: "session", HeadSequence: 2, HeadEventID: "terminal-event"}
	if valid.Validate() != nil {
		t.Fatal("valid fence rejected")
	}
	invalid := []TaskHeadFence{
		{},
		{Version: 1, TaskID: "task", SessionID: "session", HeadEventID: "terminal-event"},
		{Version: 1, TaskID: "bad:task", SessionID: "session", HeadSequence: 2, HeadEventID: "terminal-event"},
		{Version: 1, TaskID: "task", SessionID: "session", HeadSequence: 2, HeadEventID: "bad:event"},
	}
	for _, fence := range invalid {
		if fence.Validate() == nil {
			t.Fatal("invalid fence admitted", fence)
		}
	}
}
