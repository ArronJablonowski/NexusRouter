package sessions

import (
	"fmt"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
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

func retryableRootFixture(t *testing.T, task, retry string) []runtime.Event {
	t.Helper()
	failed := terminalHistory(t, "failed")
	out := []runtime.Event{}
	for _, event := range failed {
		if event.Kind != runtime.TaskStarted && event.Kind != runtime.TurnStarted && event.Kind != runtime.TaskFailed {
			continue
		}
		event.TaskID, event.SessionID, event.CorrelationID = task, task, task
		if event.Kind == runtime.TaskStarted {
			event.Data.RetryOfTaskID = retry
		}
		if event.Kind == runtime.TaskFailed {
			event.Data.Code = "provider_retryable_no_output"
		}
		out = append(out, event)
	}
	treeLimitRenumber(out)
	return out
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
	firstCost, finalCost := .25, .5
	first[0].Data.RouteEstimatedCost = &firstCost
	h[0][0].Data.RouteEstimatedCost = &finalCost
	out, err := ProjectTerminalTree(append([][]runtime.Event{first}, h...))
	if err != nil || out.Result == nil || out.Result.TaskID != "task" || len(out.Result.PreviousTaskIDs) != 1 || out.Result.PreviousTaskIDs[0] != "first" || out.Result.RouteEstimatedCost == nil || *out.Result.RouteEstimatedCost != .75 || out.Result.Usage != nil {
		t.Fatal(out, err)
	}
}

func TestProjectTerminalTreeSupportsContiguousFallbackChain(t *testing.T) {
	for _, retries := range []int{3, MaxTerminalRouteAttempts - 1} {
		t.Run(fmt.Sprint(retries), func(t *testing.T) {
			histories := [][]runtime.Event{}
			previous := ""
			for i := 0; i < retries; i++ {
				id := fmt.Sprintf("retry-%d", i)
				histories = append(histories, retryableRootFixture(t, id, previous))
				previous = id
			}
			final := terminalHistory(t, "success")
			for i := range final {
				final[i].TaskID, final[i].SessionID, final[i].CorrelationID = "final", "final", "final"
			}
			final[0].Data.RetryOfTaskID = previous
			treeLimitRenumber(final)
			histories = append(histories, final)
			out, err := ProjectTerminalTree(histories)
			if err != nil || out.State != "succeeded" || out.Result == nil || out.Result.TaskID != "final" || len(out.Result.PreviousTaskIDs) != retries || out.Result.PreviousTaskIDs[0] != "retry-0" || out.Result.PreviousTaskIDs[retries-1] != fmt.Sprintf("retry-%d", retries-1) {
				t.Fatal(out, err)
			}
		})
	}
}

func TestProjectTerminalTreeRejectsUnsafeFallbackChain(t *testing.T) {
	for _, mode := range []string{"broken-lineage", "intermediate-output", "too-many-roots"} {
		t.Run(mode, func(t *testing.T) {
			count := 3
			if mode == "too-many-roots" {
				count = MaxTerminalRouteAttempts + 1
			}
			histories := [][]runtime.Event{}
			previous := ""
			for i := 0; i < count; i++ {
				id := fmt.Sprintf("retry-%d", i)
				history := retryableRootFixture(t, id, previous)
				histories = append(histories, history)
				previous = id
			}
			if mode == "broken-lineage" {
				histories[2][0].Data.RetryOfTaskID = "retry-0"
			}
			if mode == "intermediate-output" {
				delta := histories[1][1]
				delta.Kind, delta.Data = runtime.ModelDelta, runtime.Data{Text: "untrusted partial"}
				histories[1] = append(histories[1][:2], append([]runtime.Event{delta}, histories[1][2:]...)...)
				treeLimitRenumber(histories[1])
			}
			final := terminalHistory(t, "success")
			for i := range final {
				final[i].TaskID, final[i].SessionID, final[i].CorrelationID = "final", "final", "final"
			}
			final[0].Data.RetryOfTaskID = previous
			treeLimitRenumber(final)
			histories = append(histories, final)
			if out, err := ProjectTerminalTree(histories); err == nil || out.Result != nil {
				t.Fatal("unsafe fallback chain accepted", out, err)
			}
		})
	}
}

func TestProjectTerminalTreeRouteLimitAllowsFinalDelegation(t *testing.T) {
	histories := [][]runtime.Event{}
	previous := ""
	for i := 0; i < MaxTerminalRouteAttempts-1; i++ {
		id := fmt.Sprintf("retry-%d", i)
		histories = append(histories, retryableRootFixture(t, id, previous))
		previous = id
	}
	delegated := terminalTreeFixture(t)
	delegated[0][0].Data.RetryOfTaskID = previous
	histories = append(histories, delegated...)
	out, err := ProjectTerminalTree(histories)
	if err != nil || out.State != "succeeded" || out.Result == nil || out.Result.TaskID != "task" || len(out.Result.PreviousTaskIDs) != MaxTerminalRouteAttempts-1 {
		t.Fatal(out, err)
	}
}

func TestProjectTerminalTreeStreamFailureBoundary(t *testing.T) {
	for _, mode := range []string{"text-only", "legacy-code", "tool-proposal", "steering", "second-turn", "completed-turn"} {
		t.Run(mode, func(t *testing.T) {
			failed := retryableRootFixture(t, "prior", "")
			failed[len(failed)-1].Data.Code = "provider_failed_before_tools"
			delta := failed[1]
			delta.ID = "partial"
			delta.Kind = runtime.ModelDelta
			delta.Data = runtime.Data{Text: "partial evidence"}
			if mode == "legacy-code" {
				failed[len(failed)-1].Data.Code = "provider_retryable_no_output"
			}
			switch mode {
			case "tool-proposal":
				delta.Data.ToolCalls = []providers.ToolCall{{ID: "tool", Name: "read", Arguments: []byte(`{}`)}}
			case "steering":
				delta.Kind = runtime.SteeringApplied
				delta.Data.SteeringID = "steering"
				delta.TurnID = ""
				delta.AttemptID = ""
			case "second-turn":
				delta.Kind = runtime.TurnStarted
				delta.Data = failed[1].Data
				delta.TurnID = "second"
				delta.AttemptID = "second"
			case "completed-turn":
				delta.Kind = runtime.TurnCompleted
				delta.Data.FinishReason = "stop"
			}
			failed = append(failed[:2], append([]runtime.Event{delta}, failed[2:]...)...)
			treeLimitRenumber(failed)
			final := terminalHistory(t, "success")
			final[0].Data.RetryOfTaskID = "prior"
			out, err := ProjectTerminalTree([][]runtime.Event{failed, final})
			if mode == "text-only" {
				if err != nil || out.Result == nil || len(out.Result.PreviousTaskIDs) != 1 {
					t.Fatal(out, err)
				}
			} else if err == nil {
				t.Fatal("unsafe boundary accepted", mode, out)
			}
		})
	}
}
