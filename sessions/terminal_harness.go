package sessions

import "github.com/ArronJablonowski/NexusRouter/runtime"

// A native task is one durable harness operation, not a fabricated model turn.
// Recovery consumes its bound terminal result without inferring quality.
func projectNativeTerminal(events []runtime.Event, out TerminalOutcome) (TerminalOutcome, error) {
	bad := func() (TerminalOutcome, error) { return TerminalOutcome{}, ErrHistory }
	if len(events) > 0 && events[0].Data.Harness != nil && events[0].Data.Harness.Protocol == runtime.HarnessAgentProtocol {
		return projectNativeAgentTerminal(events, out)
	}
	if len(events) != 2 {
		return bad()
	}
	start, last := events[0], events[1]
	for _, e := range events {
		if e.Validate() != nil || e.TurnID != "" || e.AttemptID != "" || e.Data.Accepted != nil {
			return bad()
		}
	}
	if start.Sequence != 1 || last.Sequence != 2 || start.Data.Validation != "" {
		return bad()
	}
	if out.State != "succeeded" {
		if last.Data.HarnessOutcome != nil || last.Data.Text != "" {
			return bad()
		}
		return out, nil
	}
	if _, err := runtime.ValidateHarnessOutcome(events, start.TaskID); err != nil {
		return bad()
	}
	if usage := last.Data.Usage; usage != nil {
		if usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.InputTokens > 1<<40 || usage.OutputTokens > 1<<40 {
			return bad()
		}
		copy := *usage
		out.Result.Usage = &copy
	}
	out.Result.HarnessEvidenceStatus = "not_recovered"
	out.Result.Text = last.Data.Text
	out.Result.Turns = 1
	out.Result.FinishReason = "stop"
	return out, nil
}

func projectNativeAgentTerminal(events []runtime.Event, out TerminalOutcome) (TerminalOutcome, error) {
	usage, err := runtime.ValidateHarnessAgentJournal(events, events[0].TaskID)
	if err != nil || events[0].Data.Validation != "" {
		return TerminalOutcome{}, ErrHistory
	}
	out.Result.Usage = usage
	out.Result.HarnessEvidenceStatus = "not_recovered"
	for _, e := range events {
		if e.Kind == runtime.TurnStarted {
			out.Result.Turns++
		}
	}
	if out.State == "succeeded" {
		if _, err := runtime.ValidateHarnessOutcome(events, events[0].TaskID); err != nil {
			return TerminalOutcome{}, ErrHistory
		}
		out.Result.Text = events[len(events)-1].Data.Text
		out.Result.FinishReason = "stop"
	}
	return out, nil
}
