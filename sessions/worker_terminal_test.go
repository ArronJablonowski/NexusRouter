package sessions

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func workerTerminalFixture() []runtime.Event {
	kinds := []runtime.Kind{runtime.TaskStarted, runtime.WorkerStarted, runtime.WorkerHeartbeat, runtime.EvaluationRecorded, runtime.WorkerCompleted, runtime.TaskCompleted}
	events := make([]runtime.Event, len(kinds))
	for i, k := range kinds {
		events[i] = runtime.Event{Version: 1, ID: fmt.Sprint("event", i), TaskID: "worker-task", SessionID: "session", CorrelationID: "worker-task", WorkerID: "worker", Sequence: int64(i + 1), Time: time.Now().UTC(), Kind: k}
	}
	events[0].Data = runtime.Data{ParentTaskID: "parent", SubmissionID: "submission"}
	accepted := true
	events[3].Data = runtime.Data{Accepted: &accepted, Code: "worker_validator"}
	events[4].Data.Text = "validated answer"
	return events
}

func TestProjectWorkerTerminal(t *testing.T) {
	text, complete, err := projectWorkerTerminal(workerTerminalFixture())
	if err != nil || !complete || text != "validated answer" {
		t.Fatal(text, complete, err)
	}
	for _, kind := range []runtime.Kind{runtime.TaskFailed, runtime.TaskCanceled} {
		for _, prefix := range []int{1, 2, 3, 4, 5} {
			events := workerTerminalFixture()[:prefix]
			end := events[0]
			end.ID = "terminal"
			end.Sequence = int64(prefix + 1)
			end.Kind = kind
			end.Data = runtime.Data{Code: "worker_failed"}
			events = append(events, end)
			text, complete, err := projectWorkerTerminal(events)
			if err != nil || complete || text != "" {
				t.Fatal(kind, prefix, text, complete, err)
			}
		}
	}
}

func TestProjectWorkerTerminalRejects(t *testing.T) {
	for _, mode := range []string{"running", "parent", "submission", "worker", "correlation", "model", "provider", "gap", "duplicate", "heartbeat-after-check", "missing-start", "bad-check", "rejected", "missing-output", "blank", "utf8", "large-output", "large-history", "too-many", "tool"} {
		t.Run(mode, func(t *testing.T) {
			e := workerTerminalFixture()
			switch mode {
			case "running":
				e = e[:1]
			case "parent":
				e[0].Data.ParentTaskID = ""
			case "submission":
				e[0].Data.SubmissionID = ""
			case "worker":
				e[2].WorkerID = "other"
			case "correlation":
				e[2].CorrelationID = "other"
			case "model":
				e[2].Data.ModelID = "model"
			case "provider":
				e[2].Data.ProviderID = "provider"
			case "gap":
				e[2].Sequence++
			case "duplicate":
				e[2].Kind = runtime.WorkerStarted
			case "heartbeat-after-check":
				e[4].Kind = runtime.WorkerHeartbeat
			case "missing-start":
				e[1].Kind = runtime.WorkerHeartbeat
			case "bad-check":
				e[3].Data.Code = "other"
			case "rejected":
				v := false
				e[3].Data.Accepted = &v
			case "missing-output":
				e[4].Kind = runtime.TaskCompleted
			case "blank":
				e[4].Data.Text = " "
			case "utf8":
				e[4].Data.Text = string([]byte{255})
			case "large-output":
				e[4].Data.Text = strings.Repeat("x", (64<<10)+1)
			case "large-history":
				e[0].Data.Text = strings.Repeat("x", 8<<20)
			case "too-many":
				e = make([]runtime.Event, 10001)
			case "tool":
				e[2].Kind = runtime.TurnStarted
			}
			text, complete, err := projectWorkerTerminal(e)
			if !errors.Is(err, ErrHistory) || text != "" || complete {
				t.Fatal(text, complete, err)
			}
		})
	}
}
