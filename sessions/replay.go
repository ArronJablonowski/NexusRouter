// Package sessions reconstructs conversation state from durable runtime facts.
package sessions

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

var ErrHistory = errors.New("invalid session history")

type Reader interface {
	Read(context.Context, string, int64, int) ([]runtime.Event, error)
}
type Pending struct {
	Call         providers.ToolCall
	TurnID       string
	AttemptID    string
	Dispatched   bool
	ToolBehavior runtime.ToolBehavior
}
type Snapshot struct {
	SkillContext             *runtime.SkillContextUse `json:"SkillContext,omitempty"`
	Compaction               *runtime.ContextCompaction
	ContextLineage           *runtime.ContextLineage `json:"ContextLineage,omitempty"`
	RetryOfTaskID            string
	ParentTaskID, Privacy    string
	TaskID, SessionID, State string
	Sequence                 int64
	Messages                 []providers.Message
	MessageSequences         []int64
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
	steeringIDs := map[string]bool{}
	initialMessages := 0
	completedTurn := false
	responseRevisions := 0
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
				s.Compaction = e.Data.Compaction
				s.ContextLineage = e.Data.ContextLineage
				s.SkillContext = e.Data.SkillContext.Clone()
				s.Messages = e.Data.Messages
				initialMessages = len(e.Data.Messages)
				s.MessageSequences = make([]int64, len(s.Messages))
				for i := range s.MessageSequences {
					s.MessageSequences[i] = e.Sequence
				}
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
				if s.ContextLineage != nil {
					for _, id := range s.ContextLineage.ToolCallIDs {
						toolIDs[id] = true
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
			case runtime.ResponseRevision:
				if turn != "" || attempt != "" || len(s.Pending) > 0 || s.UncertainEffects || !completedTurn || responseRevisions >= 2 || len(s.Messages) == 0 || s.Messages[len(s.Messages)-1].Role != "assistant" || len(s.Messages[len(s.Messages)-1].ToolCalls) != 0 {
					return s, ErrHistory
				}
				responseRevisions++
				s.Messages = append(s.Messages, providers.Message{Role: "user", Content: e.Data.Text})
				s.MessageSequences = append(s.MessageSequences, e.Sequence)
			case runtime.SteeringApplied:
				if turn != "" || attempt != "" || len(s.Pending) > 0 || s.UncertainEffects || e.TurnID != "" || e.AttemptID != "" || steeringIDs[e.Data.SteeringID] || len(steeringIDs) >= 32 {
					return s, ErrHistory
				}
				steeringIDs[e.Data.SteeringID] = true
				s.Messages = append(s.Messages, providers.Message{Role: "user", Content: e.Data.Text})
				s.MessageSequences = append(s.MessageSequences, e.Sequence)
			case runtime.ContextCompacted:
				// Compaction is a one-shot replacement of the exact initial
				// continuation prefix. Everything appended by this task remains
				// in the suffix, including steering and complete tool batches.
				if turn != "" || attempt != "" || !completedTurn || len(s.Pending) > 0 || s.UncertainEffects || s.Compaction != nil && (s.ContextLineage == nil || e.Data.ContextLineage == nil) || e.TurnID != "" || e.AttemptID != "" || e.Data.Compaction == nil || e.Data.ParentTaskID != s.ParentTaskID || initialMessages < 1 || e.Data.ReplacedMessages != initialMessages || e.Data.ReplacedMessages > len(s.Messages) || len(e.Data.Messages) == 0 {
					return s, ErrHistory
				}
				if s.ContextLineage != nil || e.Data.ContextLineage != nil {
					expected, lineageErr := runtime.ExtendContextLineage(s.ContextLineage, task, e.Sequence, e.Data.Compaction, s.Messages)
					if lineageErr != nil || e.Data.ContextLineage == nil || expected.Digest != e.Data.ContextLineage.Digest {
						return s, ErrHistory
					}
				}
				candidate := append([]providers.Message(nil), e.Data.Messages...)
				candidate = append(candidate, s.Messages[e.Data.ReplacedMessages:]...)
				if providers.ValidateMessages(candidate) != nil {
					return s, ErrHistory
				}
				// Retain identities from the removed prefix and union identities
				// introduced by the replacement. A corrupt journal must not be
				// able to reuse either identity in a later completed turn even
				// when the resulting duplicate would only become visible after
				// activation.
				for _, message := range e.Data.Messages {
					for _, call := range message.ToolCalls {
						toolIDs[call.ID] = true
					}
				}
				sequences := make([]int64, len(e.Data.Messages))
				for i := range sequences {
					sequences[i] = e.Sequence
				}
				sequences = append(sequences, s.MessageSequences[e.Data.ReplacedMessages:]...)
				s.Messages, s.MessageSequences, s.Compaction, s.ContextLineage = candidate, sequences, e.Data.Compaction, e.Data.ContextLineage
				initialMessages = len(e.Data.Messages)
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
				s.MessageSequences = append(s.MessageSequences, e.Sequence)
				turn = ""
				attempt = ""
				completedTurn = true
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
					pending.ToolBehavior = e.Data.ToolBehavior
					s.Pending[e.Data.ToolCallID] = pending
				} else {
					if !pending.Dispatched || pending.ToolBehavior != e.Data.ToolBehavior {
						return s, ErrHistory
					}
					s.Messages = append(s.Messages, providers.Message{Role: "tool", ToolCallID: e.Data.ToolCallID, Content: e.Data.Text, ToolFailed: e.Data.Code == "tool_failed"})
					s.MessageSequences = append(s.MessageSequences, e.Sequence)
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
