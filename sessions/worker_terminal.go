package sessions

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

// projectWorkerTerminal accepts only the supervisor's bounded terminal lifecycle,
// never model/tool execution histories or an unacknowledged worker result.
func projectWorkerTerminal(events []runtime.Event) (text string, completed bool, err error) {
	bad := func() (string, bool, error) { return "", false, ErrHistory }
	if len(events) < 1 || len(events) > 10000 {
		return bad()
	}
	start := events[0]
	if start.Kind != runtime.TaskStarted || start.WorkerID == "" || !ValidEventPageID(start.Data.ParentTaskID) || !ValidEventPageID(start.Data.SubmissionID) {
		return bad()
	}
	total, stage := 0, 0
	for i, e := range events {
		body, encodeErr := e.Encode()
		if encodeErr != nil || len(body) > (8<<20)-total {
			return bad()
		}
		total += len(body)
		if e.CorrelationID != e.TaskID || e.WorkerID != start.WorkerID || e.Data.ModelID != "" || e.Data.ProviderID != "" || e.TurnID != "" || e.AttemptID != "" || e.Data.ToolName != "" || e.Data.ToolCallID != "" || len(e.Data.ToolCalls) != 0 {
			return bad()
		}
		switch e.Kind {
		case runtime.TaskStarted:
			if i != 0 {
				return bad()
			}
		case runtime.WorkerStarted:
			if stage != 0 || i != 1 {
				return bad()
			}
			stage = 1
		case runtime.WorkerHeartbeat:
			if stage != 1 {
				return bad()
			}
		case runtime.EvaluationRecorded:
			if stage != 1 || e.Data.Accepted == nil || !*e.Data.Accepted || e.Data.Code != "worker_validator" {
				return bad()
			}
			stage = 2
		case runtime.WorkerCompleted:
			if stage != 2 || !utf8.ValidString(e.Data.Text) || strings.TrimSpace(e.Data.Text) == "" || len(e.Data.Text) > 64<<10 {
				return bad()
			}
			stage = 3
			text = e.Data.Text
		case runtime.TaskCompleted:
			if stage != 3 || i != len(events)-1 {
				return bad()
			}
			stage = 4
			completed = true
		case runtime.TaskFailed, runtime.TaskCanceled:
			if i != len(events)-1 {
				return bad()
			}
			stage = 4
			text = ""
		default:
			return bad()
		}
	}
	if stage != 4 {
		return bad()
	}
	snapshot, replayErr := Replay(context.Background(), terminalReader(events), start.TaskID)
	if replayErr != nil || (snapshot.State != "completed" && snapshot.State != "failed" && snapshot.State != "canceled") {
		return bad()
	}
	return text, completed, nil
}
