package sessions

import (
	"context"
	"encoding/json"
	"math"
	"strings"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/submissions"
)

type TerminalOutcome struct {
	State     string
	ErrorCode string
	Result    *submissions.Result
}

type terminalReader []runtime.Event

func (r terminalReader) Read(_ context.Context, task string, after int64, limit int) ([]runtime.Event, error) {
	if after < 0 || after > int64(len(r)) || limit < 1 {
		return nil, ErrHistory
	}
	end := after + int64(limit)
	if end > int64(len(r)) {
		end = int64(len(r))
	}
	return r[after:end], nil
}

// ProjectTerminalSubmission derives only a terminal execution result from
// bounded durable facts. It neither runs models nor recreates audit outcomes.
func ProjectTerminalSubmission(events []runtime.Event) (TerminalOutcome, error) {
	bad := func() (TerminalOutcome, error) { return TerminalOutcome{}, ErrHistory }
	if len(events) < 2 || len(events) > 10000 {
		return bad()
	}
	total := 0
	for _, e := range events {
		body, err := json.Marshal(e)
		if err != nil || len(body) > 8<<20-total {
			return bad()
		}
		total += len(body)
		if e.CorrelationID != e.TaskID {
			return bad()
		}
	}
	start := events[0]
	if start.Kind != runtime.TaskStarted || !ValidEventPageID(start.Data.SubmissionID) || start.Data.ModelID == "" || start.Data.ProviderID == "" {
		return bad()
	}
	snapshot, err := Replay(context.Background(), terminalReader(events), start.TaskID)
	if err != nil {
		return bad()
	}
	last := events[len(events)-1]
	out := TerminalOutcome{Result: &submissions.Result{TaskID: start.TaskID, AuditStatus: "not_recovered"}}
	switch snapshot.State {
	case "completed":
		if last.Kind != runtime.TaskCompleted || snapshot.InterruptedTurn || snapshot.UncertainEffects || len(snapshot.Pending) > 0 {
			return bad()
		}
		out.State = "succeeded"
	case "failed":
		if last.Kind != runtime.TaskFailed {
			return bad()
		}
		out.State, out.ErrorCode = "failed", "execution_failed"
	case "canceled":
		if last.Kind != runtime.TaskCanceled {
			return bad()
		}
		out.State, out.ErrorCode = "canceled", "canceled"
	default:
		return bad()
	}
	usageKnown := true
	completed := 0
	usage := providers.Usage{}
	var turnStart, turnEnd runtime.Event
	for _, e := range events {
		switch e.Kind {
		case runtime.TurnStarted:
			if e.Data.ModelID != start.Data.ModelID || e.Data.ProviderID != start.Data.ProviderID || e.Data.ModelID == "" || e.Data.ProviderID == "" {
				return bad()
			}
			out.Result.Turns++
			turnStart = e
		case runtime.TurnCompleted:
			completed++
			turnEnd = e
			u := e.Data.Usage
			if u == nil || u.InputTokens < 0 || u.OutputTokens < 0 {
				usageKnown = false
			} else if u.InputTokens > math.MaxInt64-usage.InputTokens || u.OutputTokens > math.MaxInt64-usage.OutputTokens {
				usageKnown = false
			} else {
				usage.InputTokens += u.InputTokens
				usage.OutputTokens += u.OutputTokens
			}
		}
	}
	if usageKnown && out.Result.Turns > 0 && completed == out.Result.Turns {
		out.Result.Usage = &usage
	}
	if out.State != "succeeded" {
		return out, nil
	}
	if turnEnd.Kind != runtime.TurnCompleted || turnStart.AttemptID != turnEnd.AttemptID || turnStart.TurnID != turnEnd.TurnID || last.AttemptID != turnEnd.AttemptID || last.TurnID != turnEnd.TurnID || len(turnEnd.Data.ToolCalls) > 0 || turnEnd.Data.FinishReason != "stop" || strings.TrimSpace(turnEnd.Data.Text) == "" || turnStart.Data.ModelID == "" || turnStart.Data.ProviderID == "" {
		return bad()
	}
	if start.Data.Validation != "" && start.Data.Validation != "go_source" {
		return bad()
	}
	nonempty, syntax := int64(0), int64(0)
	for _, e := range events {
		if e.Kind == runtime.SteeringApplied && e.Sequence > turnEnd.Sequence {
			return bad()
		}
		if e.Kind != runtime.EvaluationRecorded {
			continue
		}
		// A prior answer can have been evaluated before newly queued steering
		// forced another turn. Only the final turn's evaluation window supplies
		// acceptance evidence; checks inside that window must still match it.
		if e.Sequence <= turnStart.Sequence {
			continue
		}
		if e.Data.Code != "deterministic.nonempty_text.v1" && e.Data.Code != "deterministic.go_syntax.v1" {
			continue
		}
		if e.AttemptID != turnEnd.AttemptID || e.TurnID != turnEnd.TurnID || e.Sequence <= turnEnd.Sequence || e.Sequence >= last.Sequence || e.Data.ModelID != turnStart.Data.ModelID || e.Data.ProviderID != turnStart.Data.ProviderID || e.Data.Domain != start.Data.Domain || e.Data.Profile != start.Data.Profile || e.Data.Accepted == nil || !*e.Data.Accepted {
			return bad()
		}
		if e.Data.Code == "deterministic.nonempty_text.v1" {
			if nonempty != 0 {
				return bad()
			}
			nonempty = e.Sequence
		} else {
			if syntax != 0 {
				return bad()
			}
			syntax = e.Sequence
		}
	}
	if nonempty == 0 {
		return bad()
	}
	if start.Data.Validation == "go_source" {
		if syntax <= nonempty || !evaluation.GoSourceValid(turnEnd.Data.Text) {
			return bad()
		}
	} else if syntax != 0 {
		return bad()
	}
	out.Result.Text = turnEnd.Data.Text
	out.Result.FinishReason = turnEnd.Data.FinishReason
	return out, nil
}
