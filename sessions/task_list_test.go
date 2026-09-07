package sessions

import (
	"strings"
	"testing"
	"time"
)

func taskSummaryFixture(id string) TaskSummary {
	return TaskSummary{Version: 1, TaskID: id, SessionID: "session-" + id, State: "completed", Sequence: 2, StartedAt: time.Unix(100, 0)}
}

func TestTaskListContracts(t *testing.T) {
	cursor := TaskListCursor{Version: 1, Last: 2, HighWater: 3, State: "completed"}
	encoded, err := EncodeTaskListCursor(cursor)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeTaskListCursor(encoded)
	if err != nil || decoded != cursor {
		t.Fatal(decoded, err)
	}
	options := TaskListOptions{State: "completed", After: encoded, Limit: 25}
	if options.Validate() != nil {
		t.Fatal(options)
	}
	page := TaskPage{Version: 1, Items: []TaskSummary{taskSummaryFixture("task")}, HasMore: true, NextCursor: encoded}
	if page.Validate() != nil {
		t.Fatal(page)
	}
	for _, bad := range []TaskListOptions{
		{Limit: 0}, {Limit: 101}, {State: "queued", Limit: 1},
		{State: "running", After: encoded, Limit: 1}, {After: strings.Repeat("x", 1025), Limit: 1},
	} {
		if bad.Validate() == nil {
			t.Fatal("invalid options accepted", bad)
		}
	}
}

func TestTaskPageRejectsMalformedMetadata(t *testing.T) {
	valid := taskSummaryFixture("task")
	for _, mutate := range []func(*TaskPage){
		func(p *TaskPage) { p.Items[0].TaskID = "bad id" },
		func(p *TaskPage) { p.Items[0].State = "queued" },
		func(p *TaskPage) { p.Items[0].Sequence = 10001 },
		func(p *TaskPage) { p.Items = append(p.Items, p.Items[0]) },
		func(p *TaskPage) { p.HasMore = true },
	} {
		page := TaskPage{Version: 1, Items: []TaskSummary{valid}}
		mutate(&page)
		if page.Validate() == nil {
			t.Fatal("invalid page accepted", page)
		}
	}
}
