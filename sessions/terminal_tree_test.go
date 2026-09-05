package sessions

import (
	"fmt"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func terminalTreeFixture(t *testing.T) [][]runtime.Event {
	t.Helper()
	root := terminalHistory(t, "success")
	child := terminalHistory(t, "success")
	root[0].Data.Privacy, child[0].Data.Privacy = "local_only", "local_only"
	for i := range child {
		child[i].TaskID = "child"
		child[i].SessionID = "child"
		child[i].CorrelationID = "child"
	}
	child[0].Data.ParentTaskID = "work"
	yes := true
	work := []runtime.Event{}
	for i, kind := range []runtime.Kind{runtime.TaskStarted, runtime.WorkerStarted, runtime.EvaluationRecorded, runtime.WorkerCompleted, runtime.TaskCompleted} {
		e := runtime.Event{Version: 1, ID: fmt.Sprintf("work-event-%d", i), TaskID: "work", SessionID: "task", CorrelationID: "work", WorkerID: "worker", Sequence: int64(i + 1), Time: time.Now().UTC(), Kind: kind}
		if kind == runtime.TaskStarted {
			e.Data.ParentTaskID = "task"
			e.Data.SubmissionID = "submission"
		}
		if kind == runtime.EvaluationRecorded {
			e.Data.Code = "worker_validator"
			e.Data.Accepted = &yes
		}
		if kind == runtime.WorkerCompleted {
			e.Data.Text = "answer"
		}
		work = append(work, e)
	}
	return [][]runtime.Event{root, work, child}
}

func TestProjectTerminalTreeReturnsOnlyRoot(t *testing.T) {
	histories := terminalTreeFixture(t)
	for _, ordered := range [][][]runtime.Event{histories, {histories[2], histories[0], histories[1]}} {
		out, err := ProjectTerminalTree(ordered)
		if err != nil || out.State != "succeeded" || out.Result == nil || out.Result.TaskID != "task" || out.Result.Text != "answer" || len(out.Result.PreviousTaskIDs) != 0 || out.Result.Turns != 1 {
			t.Fatal(out, err)
		}
	}
}

func TestProjectTerminalTreeRejectsMalformedGraphs(t *testing.T) {
	for _, mode := range []string{"mismatch_text", "missing_child", "cycle", "unknown_work_parent", "wrong_submission", "duplicate", "two_children", "missing_worker_validation", "negative_worker_validation", "unvalidated_child", "unknown_child_parent", "privacy", "unknown_privacy", "session"} {
		t.Run(mode, func(t *testing.T) {
			h := terminalTreeFixture(t)
			switch mode {
			case "privacy":
				h[2][0].Data.Privacy = "cloud_allowed"
			case "unknown_privacy":
				h[2][0].Data.Privacy = ""
			case "session":
				for i := range h[1] {
					h[1][i].SessionID = "different"
				}
			case "mismatch_text":
				h[1][3].Data.Text = "invented"
			case "missing_child":
				h = h[:2]
			case "cycle":
				h[0][0].Data.ParentTaskID = "child"
			case "unknown_work_parent":
				h[1][0].Data.ParentTaskID = "missing"
			case "wrong_submission":
				h[2][0].Data.SubmissionID = "other"
			case "duplicate":
				h = append(h, h[2])
			case "two_children":
				other := append([]runtime.Event(nil), h[2]...)
				for i := range other {
					other[i].TaskID = "child2"
					other[i].SessionID = "child2"
					other[i].CorrelationID = "child2"
					other[i].ID = "other-" + other[i].ID
				}
				h = append(h, other)
			case "missing_worker_validation":
				h[1][2].Data.Code = "unrecognized"
			case "negative_worker_validation":
				no := false
				h[1][2].Data.Accepted = &no
			case "unvalidated_child":
				for i := range h[2] {
					if h[2][i].Kind == runtime.EvaluationRecorded {
						h[2][i].Data.Code = "unrecognized"
					}
				}
			case "unknown_child_parent":
				h[2][0].Data.ParentTaskID = "missing"
			}
			out, err := ProjectTerminalTree(h)
			if err == nil || out.Result != nil || out.State != "" {
				t.Fatal("unproven tree accepted", out, err)
			}
		})
	}
}

func TestProjectTerminalTreeFailedWorkMayHaveNoChild(t *testing.T) {
	h := terminalTreeFixture(t)
	work := h[1][:2]
	terminal := h[1][4]
	terminal.Sequence = 3
	terminal.Kind = runtime.TaskFailed
	terminal.Data = runtime.Data{Code: "worker_failed"}
	work = append(work, terminal)
	out, err := ProjectTerminalTree([][]runtime.Event{h[0], work})
	if err != nil || out.State != "succeeded" || out.Result == nil || out.Result.TaskID != "task" {
		t.Fatal(out, err)
	}
}

func TestProjectTerminalTreeFallbackPreviousIDsExcludeDelegates(t *testing.T) {
	h := terminalTreeFixture(t)
	failed := terminalHistory(t, "failed")
	var first []runtime.Event
	for _, event := range failed {
		if event.Kind != runtime.TaskStarted && event.Kind != runtime.TurnStarted && event.Kind != runtime.TaskFailed {
			continue
		}
		event.TaskID, event.SessionID, event.CorrelationID = "first", "first", "first"
		event.Sequence = int64(len(first) + 1)
		if event.Kind == runtime.TaskFailed {
			event.Data.Code = "provider_retryable_no_output"
		}
		first = append(first, event)
	}
	h[0][0].Data.RetryOfTaskID = "first"
	out, err := ProjectTerminalTree(append([][]runtime.Event{first}, h...))
	if err != nil || out.Result == nil || out.Result.TaskID != "task" || len(out.Result.PreviousTaskIDs) != 1 || out.Result.PreviousTaskIDs[0] != "first" {
		t.Fatal(out, err)
	}
}
