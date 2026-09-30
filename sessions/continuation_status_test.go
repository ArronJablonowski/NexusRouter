package sessions

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func continuationStatusFixture() (Snapshot, []runtime.Event) {
	snapshot := Snapshot{TaskID: "task", SessionID: "session", Sequence: 6, State: "failed"}
	tool := runtime.Event{ID: "tool-result", TaskID: "task", SessionID: "session", Sequence: 5, Kind: runtime.ToolCompleted, TurnID: "turn", AttemptID: "attempt", Data: runtime.Data{ToolCallID: "call", ToolName: "delegate", Effect: runtime.NoEffect, Code: "delegation_recovered", Text: "PRIVATE_RESULT"}}
	end := runtime.Event{ID: "terminal", TaskID: "task", SessionID: "session", Sequence: 6, Kind: runtime.TaskFailed, TurnID: "turn", AttemptID: "attempt", CausationID: tool.ID, Data: runtime.Data{Code: "interrupted_after_delegation"}}
	return snapshot, []runtime.Event{tool, end}
}

func TestAssessContinuationStatesAndSafetyPrecedence(t *testing.T) {
	for _, state := range []string{"running", "completed", "failed", "canceled"} {
		for _, hazard := range []string{"none", "pending", "uncertain", "interrupted", "all"} {
			t.Run(state+"/"+hazard, func(t *testing.T) {
				s := Snapshot{TaskID: "task", SessionID: "session", Sequence: 4, State: state, Messages: []providers.Message{{Role: "user", Content: "PRIVATE_PROMPT"}}}
				if hazard == "pending" || hazard == "all" {
					s.Pending = map[string]Pending{"call": {}}
				}
				if hazard == "uncertain" || hazard == "all" {
					s.UncertainEffects = true
				}
				if hazard == "interrupted" || hazard == "all" {
					s.InterruptedTurn = true
				}
				out := AssessContinuation(s, nil)
				want := map[string]string{"running": "task_running", "completed": "completed", "failed": "task_failed", "canceled": "task_canceled"}[state]
				switch hazard {
				case "pending", "all":
					want = "pending_tools"
				case "uncertain":
					want = "uncertain_effects"
				case "interrupted":
					want = "interrupted_turn"
				}
				if out.Validate() != nil || out.Reason != want || out.HistoryEligible != (state == "completed" && hazard == "none") {
					t.Fatal(out)
				}
				body, _ := json.Marshal(out)
				if strings.Contains(string(body), "PRIVATE_") {
					t.Fatal("history payload exposed")
				}
			})
		}
	}
}

func TestAssessContinuationExactRecoveredCheckpoint(t *testing.T) {
	for _, name := range []string{"single", "batch", "task", "session", "sequence", "kind", "code", "effect", "tool", "terminal task", "terminal session", "terminal sequence", "terminal kind", "terminal code", "cause", "turn", "attempt", "short", "extra", "running", "canceled", "pending", "uncertain", "interrupted"} {
		t.Run(name, func(t *testing.T) {
			s, tail := continuationStatusFixture()
			switch name {
			case "batch":
				tail[0].Data.ToolName = "delegate_batch"
			case "task":
				tail[0].TaskID = "wrong"
			case "session":
				tail[0].SessionID = "wrong"
			case "sequence":
				tail[0].Sequence--
			case "kind":
				tail[0].Kind = runtime.ToolStarted
			case "code":
				tail[0].Data.Code = "other"
			case "effect":
				tail[0].Data.Effect = runtime.UncertainEffect
			case "tool":
				tail[0].Data.ToolName = "shell"
			case "terminal task":
				tail[1].TaskID = "wrong"
			case "terminal session":
				tail[1].SessionID = "wrong"
			case "terminal sequence":
				tail[1].Sequence++
			case "terminal kind":
				tail[1].Kind = runtime.TaskCompleted
			case "terminal code":
				tail[1].Data.Code = "ordinary_failure"
			case "cause":
				tail[1].CausationID = "wrong"
			case "turn":
				tail[1].TurnID = "wrong"
			case "attempt":
				tail[1].AttemptID = "wrong"
			case "short":
				tail = tail[:1]
			case "extra":
				tail = append(tail, tail[1])
			case "running":
				s.State = "running"
			case "canceled":
				s.State = "canceled"
			case "pending":
				s.Pending = map[string]Pending{"call": {}}
			case "uncertain":
				s.UncertainEffects = true
			case "interrupted":
				s.InterruptedTurn = true
			}
			out := AssessContinuation(s, tail)
			want := name == "single" || name == "batch"
			if out.Validate() != nil || out.HistoryEligible != want || (want && out.Reason != "recovered_delegation") {
				t.Fatal(out)
			}
			body, _ := json.Marshal(out)
			if strings.Contains(string(body), "PRIVATE_RESULT") {
				t.Fatal("recovered payload exposed")
			}
		})
	}
}

func TestContinuationStatusValidationClosedVocabulary(t *testing.T) {
	base := ContinuationStatus{Version: 1, TaskID: "task", Sequence: 4, State: "completed", Reason: "completed", HistoryEligible: true}
	for _, mutation := range []string{"version", "task", "sequence", "state", "reason", "eligible mismatch", "reason state mismatch", "false completed"} {
		s := base
		switch mutation {
		case "version":
			s.Version = 2
		case "task":
			s.TaskID = ""
		case "sequence":
			s.Sequence = 0
		case "state":
			s.State = "unknown"
		case "reason":
			s.Reason = "provider_ready"
		case "eligible mismatch":
			s.Reason = "pending_tools"
		case "reason state mismatch":
			s.Reason = "recovered_delegation"
		case "false completed":
			s.HistoryEligible = false
		}
		if s.Validate() == nil {
			t.Fatal("unsafe status admitted", mutation)
		}
	}
	for _, state := range []string{"running", "completed", "failed", "canceled"} {
		s := base
		s.State = state
		s.Reason = "history_ineligible"
		s.HistoryEligible = false
		if s.Validate() != nil {
			t.Fatal("generic closed status rejected")
		}
	}
}
