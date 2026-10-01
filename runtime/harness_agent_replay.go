package runtime

import (
	"strings"
	"unicode/utf8"

	"github.com/ArronJablonowski/NexusRouter/providers"
)

const MaxHarnessAgentEvents = 386 // start + 64 provider turns + 128 tools + terminal

// ValidateHarnessAgentJournal verifies a complete native-tools-v1 journal and
// returns its once-per-turn measured usage. Nil usage means incomplete or absent
// measurement, never zero. Failed/canceled journals can retain uncertain effects;
// they cannot carry a quality outcome. Obtain events from the canonical store.
func ValidateHarnessAgentJournal(events []Event, task string) (*providers.Usage, error) {
	if len(events) < 2 || len(events) > MaxHarnessAgentEvents {
		return nil, ErrProtocol
	}
	first, last := events[0], events[len(events)-1]
	if first.Kind != TaskStarted || first.Data.Harness == nil || first.Data.Harness.Protocol != HarnessAgentProtocol || first.TurnID != "" || first.AttemptID != "" || first.Data.Usage != nil || last.TurnID != "" || last.AttemptID != "" || last.Data.Usage != nil {
		return nil, ErrProtocol
	}
	ids, turns, attempts, calls := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	var pending []providers.ToolCall
	turn, attempt, finalText := "", "", ""
	active, final, ended, fatal, toolActive := false, false, false, false, false
	behavior := ToolBehavior("")
	total := &providers.Usage{}
	measured, unknown := 0, false
	for i, e := range events {
		if e.Validate() != nil || e.Data.Accepted != nil || e.TaskID != task || e.SessionID != first.SessionID || e.CorrelationID != task || e.Sequence != int64(i+1) || ids[e.ID] || (i > 0 && e.Time.Before(events[i-1].Time)) {
			return nil, ErrProtocol
		}
		ids[e.ID] = true
		if i == 0 {
			continue
		}
		if i == len(events)-1 {
			break
		}
		if e.Data.Harness != nil || e.Data.HarnessOutcome != nil || e.Data.Accepted != nil || fatal || final {
			return nil, ErrProtocol
		}
		switch e.Kind {
		case TurnStarted:
			if active || toolActive || len(pending) > 0 || e.TurnID == "" || e.AttemptID == "" || turns[e.TurnID] || attempts[e.AttemptID] || len(turns) >= 64 || e.Data.ProviderID != first.Data.ProviderID || e.Data.ModelID != first.Data.ModelID || e.Data.Usage != nil {
				return nil, ErrProtocol
			}
			turn, attempt = e.TurnID, e.AttemptID
			turns[turn] = true
			attempts[attempt] = true
			active = true
		case TurnCompleted:
			if !active || e.TurnID != turn || e.AttemptID != attempt || !utf8.ValidString(e.Data.Text) || len(calls)+len(e.Data.ToolCalls) > 128 || len(e.Data.ToolCalls) > 128 || (ended && len(e.Data.ToolCalls) > 0) {
				return nil, ErrProtocol
			}
			if e.Data.ProviderID != "" || e.Data.ModelID != "" {
				return nil, ErrProtocol
			}
			if len(e.Data.ToolCalls) == 0 {
				if e.Data.FinishReason != "stop" || strings.TrimSpace(e.Data.Text) == "" {
					return nil, ErrProtocol
				}
				final = true
				finalText = e.Data.Text
			} else if e.Data.FinishReason != "tool_calls" {
				return nil, ErrProtocol
			}
			for _, c := range e.Data.ToolCalls {
				if c.ID == "" || len(c.ID) > 256 || c.Name == "" || len(c.Name) > 64 || calls[c.ID] || len(c.Arguments) > 64<<10 || !utf8.Valid(c.Arguments) {
					return nil, ErrProtocol
				}
				if _, err := canonicalToolArguments(c.Arguments); err != nil {
					return nil, ErrProtocol
				}
				calls[c.ID] = true
			}
			active = false
			pending = e.Data.ToolCalls
			if e.Data.Usage == nil {
				unknown = true
			} else {
				u := e.Data.Usage
				if u.InputTokens < 0 || u.OutputTokens < 0 || u.InputTokens > 1<<40 || u.OutputTokens > 1<<40 {
					return nil, ErrProtocol
				}
				total.InputTokens += u.InputTokens
				total.OutputTokens += u.OutputTokens
				measured++
			}
		case ToolStarted, ToolCompleted:
			if active || len(pending) == 0 || e.TurnID != turn || e.AttemptID != attempt || e.Data.ToolCallID != pending[0].ID || e.Data.ToolName != pending[0].Name || !e.Data.ToolBehavior.Valid() || e.Data.Usage != nil {
				return nil, ErrProtocol
			}
			if e.Kind == ToolStarted {
				if toolActive || ended || e.Data.Effect != UncertainEffect || e.Data.Text != "" || e.Data.Code != "" {
					return nil, ErrProtocol
				}
				toolActive = true
				behavior = e.Data.ToolBehavior
			} else {
				if !toolActive || e.Data.ToolBehavior != behavior {
					return nil, ErrProtocol
				}
				toolActive = false
				pending = pending[1:]
				switch e.Data.Code {
				case "":
				case "tool_use_ended":
					ended = true
				case "tool_failed":
					fatal = true
				case "tool_failed_recoverable":
					if e.Data.Effect != NoEffect {
						return nil, ErrProtocol
					}
				default:
					return nil, ErrProtocol
				}
				if e.Data.Effect == UncertainEffect || (behavior == BehaviorReadOnly && e.Data.Effect == ConfirmedEffect) {
					fatal = true
				}
				if ended && len(pending) > 0 {
					fatal = true
				}
			}
		default:
			return nil, ErrProtocol
		}
	}
	switch last.Kind {
	case TaskCompleted:
		o := last.Data.HarnessOutcome
		if !final || active || toolActive || len(pending) > 0 || fatal || o == nil || o.Actual != first.Data.Harness.Identity || o.Task != first.Data.Harness.Task || last.Data.Text != finalText || last.Data.Code != "" {
			return nil, ErrProtocol
		}
	case TaskFailed, TaskCanceled:
		if last.Data.HarnessOutcome != nil || last.Data.Text != "" {
			return nil, ErrProtocol
		}
		if last.Kind == TaskFailed && last.Data.Code != "harness_failed" || last.Kind == TaskCanceled && last.Data.Code != "harness_canceled" {
			return nil, ErrProtocol
		}
	default:
		return nil, ErrProtocol
	}
	if active || measured == 0 || unknown {
		return nil, nil
	}
	return total, nil
}
