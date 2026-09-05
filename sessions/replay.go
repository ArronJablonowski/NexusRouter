// Package sessions reconstructs conversation state from durable runtime facts.
package sessions

import (
	"context"
	"encoding/json"
	"errors"

	"darwinrouter/providers"
	"darwinrouter/runtime"
)

var ErrHistory = errors.New("invalid session history")

type Reader interface {
	Read(context.Context, string, int64, int) ([]runtime.Event, error)
}
type Pending struct {
	Call       providers.ToolCall
	TurnID     string
	AttemptID  string
	Dispatched bool
}
type Snapshot struct {
	RetryOfTaskID            string
	ParentTaskID, Privacy    string
	TaskID, SessionID, State string
	Sequence                 int64
	Messages                 []providers.Message
	Pending                  map[string]Pending
	InterruptedTurn          bool
	UncertainEffects         bool
}

// Replay reads all pages, ignoring partial model deltas. Only completed turns
// become assistant messages. Unfinished dispatches remain explicitly uncertain;
// callers must not infer permission to retry from the absence of a result.
func Replay(ctx context.Context, r Reader, task string) (Snapshot, error) {
	s := Snapshot{TaskID: task, Pending: map[string]Pending{}}
	if r == nil || task == "" {
		return s, ErrHistory
	}
	turn, attempt := "", ""
	seen := map[string]bool{}
	toolIDs := map[string]bool{}
	turnIDs := map[string]bool{}
	attemptIDs := map[string]bool{}
	for {
		events, err := r.Read(ctx, task, s.Sequence, 100)
		if err != nil {
			return s, err
		}
		if len(events) == 0 {
			break
		}
		for _, e := range events {
			if e.Validate() != nil || e.TaskID != task || e.Sequence != s.Sequence+1 || seen[e.ID] || (s.SessionID != "" && s.SessionID != e.SessionID) || s.State == "completed" || s.State == "failed" || s.State == "canceled" {
				return s, ErrHistory
			}
			seen[e.ID] = true
			if s.Sequence == 0 && e.Kind != runtime.TaskStarted {
				return s, ErrHistory
			}
			s.Sequence = e.Sequence
			s.SessionID = e.SessionID
			switch e.Kind {
			case runtime.TaskStarted:
				if s.Sequence != 1 {
					return s, ErrHistory
				}
				s.State = "running"
				s.ParentTaskID = e.Data.ParentTaskID
				s.RetryOfTaskID = e.Data.RetryOfTaskID
				s.Privacy = e.Data.Privacy
				s.Messages = e.Data.Messages
				if len(s.Messages) > 0 {
					if providers.ValidateMessages(s.Messages) != nil {
						return s, ErrHistory
					}
					for _, m := range s.Messages {
						for _, call := range m.ToolCalls {
							toolIDs[call.ID] = true
						}
					}
				}
			case runtime.TurnStarted:
				if turn != "" || len(s.Pending) > 0 || e.AttemptID == "" || turnIDs[e.TurnID] || attemptIDs[e.AttemptID] {
					return s, ErrHistory
				}
				turn = e.TurnID
				attempt = e.AttemptID
				turnIDs[turn] = true
				attemptIDs[attempt] = true
			case runtime.ModelDelta:
				if turn == "" || turn != e.TurnID || attempt != e.AttemptID {
					return s, ErrHistory
				}
			case runtime.TurnCompleted:
				if turn == "" || turn != e.TurnID || attempt != e.AttemptID {
					return s, ErrHistory
				}
				for _, call := range e.Data.ToolCalls {
					if call.ID == "" || call.Name == "" || toolIDs[call.ID] || !json.Valid(call.Arguments) {
						return s, ErrHistory
					}
					toolIDs[call.ID] = true
					s.Pending[call.ID] = Pending{Call: call, TurnID: turn, AttemptID: attempt}
				}
				s.Messages = append(s.Messages, providers.Message{Role: "assistant", Content: e.Data.Text, ToolCalls: e.Data.ToolCalls})
				turn = ""
				attempt = ""
			case runtime.ToolStarted, runtime.ToolCompleted:
				pending, ok := s.Pending[e.Data.ToolCallID]
				if !ok || pending.Call.Name != e.Data.ToolName || pending.TurnID != e.TurnID || pending.AttemptID != e.AttemptID {
					return s, ErrHistory
				}
				if e.Kind == runtime.ToolStarted {
					if pending.Dispatched {
						return s, ErrHistory
					}
					pending.Dispatched = true
					s.Pending[e.Data.ToolCallID] = pending
				} else {
					if !pending.Dispatched {
						return s, ErrHistory
					}
					s.Messages = append(s.Messages, providers.Message{Role: "tool", ToolCallID: e.Data.ToolCallID, Content: e.Data.Text})
					delete(s.Pending, e.Data.ToolCallID)
					if e.Data.Effect == runtime.UncertainEffect {
						s.UncertainEffects = true
					}
				}
			case runtime.TaskCompleted:
				if turn != "" || len(s.Pending) > 0 || s.UncertainEffects {
					return s, ErrHistory
				}
				s.State = "completed"
			case runtime.TaskFailed:
				s.State = "failed"
			case runtime.TaskCanceled:
				s.State = "canceled"
			}
		}
	}
	if s.Sequence == 0 {
		return s, ErrHistory
	}
	s.InterruptedTurn = turn != ""
	for _, p := range s.Pending {
		if p.Dispatched {
			s.UncertainEffects = true
		}
	}
	// Detach from caller-owned reader buffers and slices.
	b, err := json.Marshal(s)
	if err != nil {
		return s, err
	}
	var copy Snapshot
	if json.Unmarshal(b, &copy) != nil {
		return s, ErrHistory
	}
	return copy, nil
}
