package sessions

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func contextCompactionReplayEvents() []runtime.Event {
	now := time.Unix(100, 0).UTC()
	summary := runtime.ContextSummary{Requirements: []string{"retain requirement"}}
	checkpoint := &runtime.ContextCompaction{Version: 1, SummaryAttemptID: "summary", SummaryReviewID: "review", SourceTaskID: "source", SourceSequence: 4, SourceDigest: strings.Repeat("a", 64), RemovedMessages: 1, Summary: summary}
	initial := []providers.Message{{Role: "user", Content: "old question"}, {Role: "assistant", Content: "old answer"}, {Role: "user", Content: "current question"}}
	replacement := []providers.Message{{Role: "system", Content: "summary warning"}, {Role: "user", Content: `{"requirements":["retain requirement"]}`}, {Role: "user", Content: "current question"}}
	event := func(id string, sequence int64, kind runtime.Kind) runtime.Event {
		return runtime.Event{Version: 1, ID: id, TaskID: "task", SessionID: "session", CorrelationID: "task", Sequence: sequence, Time: now.Add(time.Duration(sequence) * time.Second), Kind: kind}
	}
	start := event("start", 1, runtime.TaskStarted)
	start.Data.ParentTaskID, start.Data.Messages = "source", initial
	turnStart := event("turn-start", 2, runtime.TurnStarted)
	turnStart.TurnID, turnStart.AttemptID = "turn", "attempt"
	turnDone := event("turn-done", 3, runtime.TurnCompleted)
	turnDone.TurnID, turnDone.AttemptID = "turn", "attempt"
	turnDone.Data.Text, turnDone.Data.FinishReason = "live answer", "stop"
	compact := event("compact", 4, runtime.ContextCompacted)
	compact.Data.Compaction, compact.Data.ParentTaskID = checkpoint, "source"
	compact.Data.Messages, compact.Data.ReplacedMessages = replacement, len(initial)
	steer := event("steer", 5, runtime.SteeringApplied)
	steer.Data.SteeringID, steer.Data.Text = "guidance", "new guidance"
	return []runtime.Event{start, turnStart, turnDone, compact, steer}
}

func TestReplayContextCompactionReplacesOnlyFrozenPrefix(t *testing.T) {
	events := contextCompactionReplayEvents()
	snapshot, err := Replay(context.Background(), snapshotEventsForTest(events), "task")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Compaction == nil || snapshot.Compaction.SummaryAttemptID != "summary" || len(snapshot.Messages) != 5 || snapshot.Messages[0].Content != "summary warning" || snapshot.Messages[3].Content != "live answer" || snapshot.Messages[4].Content != "new guidance" {
		t.Fatal("compaction did not preserve exact live suffix", snapshot)
	}
	if len(snapshot.MessageSequences) != 5 || snapshot.MessageSequences[0] != 4 || snapshot.MessageSequences[2] != 4 || snapshot.MessageSequences[3] != 3 || snapshot.MessageSequences[4] != 5 {
		t.Fatal("message provenance changed", snapshot.MessageSequences)
	}
	// Replay returns an owned snapshot rather than aliases into the journal.
	snapshot.Messages[0].Content = "mutated"
	if events[3].Data.Messages[0].Content != "summary warning" {
		t.Fatal("snapshot aliases activation event")
	}
}

func TestReplayContextCompactionRejectsUnsafeBoundaries(t *testing.T) {
	for name, mutate := range map[string]func([]runtime.Event) []runtime.Event{
		"during turn": func(events []runtime.Event) []runtime.Event { return append(events[:2], events[3:]...) },
		"bad count":   func(events []runtime.Event) []runtime.Event { events[3].Data.ReplacedMessages++; return events },
		"before first turn": func(events []runtime.Event) []runtime.Event {
			events[3].Sequence = 2
			return []runtime.Event{events[0], events[3]}
		},
		"different source": func(events []runtime.Event) []runtime.Event {
			events[3].Data.ParentTaskID = "foreign"
			events[3].Data.Compaction.SourceTaskID = "foreign"
			return events
		},
		"second activation": func(events []runtime.Event) []runtime.Event {
			second := events[3]
			second.ID, second.Sequence = "compact-again", 5
			return append(events[:4], second)
		},
	} {
		t.Run(name, func(t *testing.T) {
			events := mutate(contextCompactionReplayEvents())
			if _, err := Replay(context.Background(), snapshotEventsForTest(events), "task"); err == nil {
				t.Fatal("unsafe activation replayed")
			}
		})
	}
}

func TestReplayContextCompactionRejectsLaterReuseOfReplacementToolCall(t *testing.T) {
	events := contextCompactionReplayEvents()[:4]
	call := providers.ToolCall{ID: "replacement-call", Name: "lookup", Arguments: json.RawMessage(`{}`)}
	events[3].Data.Messages = []providers.Message{
		{Role: "assistant", ToolCalls: []providers.ToolCall{call}},
		{Role: "tool", ToolCallID: call.ID, Content: "replacement result"},
	}
	turnStart := events[1]
	turnStart.ID, turnStart.Sequence, turnStart.TurnID, turnStart.AttemptID = "turn-start-2", 5, "turn-2", "attempt-2"
	turnDone := events[2]
	turnDone.ID, turnDone.Sequence, turnDone.TurnID, turnDone.AttemptID = "turn-done-2", 6, "turn-2", "attempt-2"
	turnDone.Data.Text, turnDone.Data.ToolCalls = "", []providers.ToolCall{call}
	events = append(events, turnStart, turnDone)

	if _, err := Replay(context.Background(), snapshotEventsForTest(events), "task"); err == nil {
		t.Fatal("later turn reused a tool-call identity introduced by compaction")
	}
}

func TestReplayContextCompactionRetainsExistingCompleteToolPair(t *testing.T) {
	events := contextCompactionReplayEvents()
	call := providers.ToolCall{ID: "retained-call", Name: "lookup", Arguments: json.RawMessage(`{}`)}
	initial := []providers.Message{
		{Role: "user", Content: "old question"},
		{Role: "assistant", ToolCalls: []providers.ToolCall{call}},
		{Role: "tool", ToolCallID: call.ID, Content: "retained result"},
		{Role: "user", Content: "current question"},
	}
	replacement := []providers.Message{
		{Role: "system", Content: "summary warning"},
		{Role: "assistant", ToolCalls: []providers.ToolCall{call}},
		{Role: "tool", ToolCallID: call.ID, Content: "retained result"},
		{Role: "user", Content: "current question"},
	}
	events[0].Data.Messages = initial
	events[3].Data.Messages = replacement
	events[3].Data.ReplacedMessages = len(initial)

	snapshot, err := Replay(context.Background(), snapshotEventsForTest(events), "task")
	if err != nil || snapshot.Compaction == nil || len(snapshot.Messages) != len(replacement)+2 {
		t.Fatal("retained source tool pair did not survive activation", snapshot, err)
	}
}

func TestInterruptedRecoveryAfterContextCompactionOnlyTerminalizes(t *testing.T) {
	events := contextCompactionReplayEvents()[:4]
	plan, err := PlanInterruptedModel([][]runtime.Event{events}, time.Unix(1000, 0).UTC(), false)
	if err != nil || len(plan.Events) != 1 || plan.Events[0].Kind != runtime.TaskFailed || plan.Events[0].Data.Code != "interrupted_model" {
		t.Fatal("activation boundary was not safely terminalized", plan, err)
	}
	full := append(append([]runtime.Event(nil), events...), plan.Events[0])
	snapshot, err := Replay(context.Background(), snapshotEventsForTest(full), "task")
	if err != nil || snapshot.State != "failed" || snapshot.Compaction == nil {
		t.Fatal("terminal recovery did not preserve activation evidence", snapshot, err)
	}
	if _, err := PlanInterruptedReadOnlyModel([][]runtime.Event{events}, time.Unix(1000, 0).UTC()); err == nil {
		t.Fatal("read-only child recovery admitted coordinator compaction")
	}
}

func TestInterruptedReadOnlyRecoveryAcceptsCompactionBoundary(t *testing.T) {
	base := contextCompactionReplayEvents()
	events := []runtime.Event{base[0], base[1]}
	call := providers.ToolCall{ID: "call", Name: "read", Arguments: json.RawMessage(`{"path":"report"}`)}
	done := base[2]
	done.Data.Text, done.Data.ToolCalls, done.Data.FinishReason = "", []providers.ToolCall{call}, "tool_calls"
	events = append(events, done)
	started := runtime.Event{Version: 1, ID: "tool-start", TaskID: "task", SessionID: "session", CorrelationID: "task", Sequence: 4, Time: time.Unix(105, 0).UTC(), Kind: runtime.ToolStarted, TurnID: "turn", AttemptID: "attempt", Data: runtime.Data{ToolCallID: "call", ToolName: "read", ToolBehavior: runtime.BehaviorReadOnly, Effect: runtime.UncertainEffect}}
	completed := started
	completed.ID, completed.Sequence, completed.Kind, completed.Time = "tool-done", 5, runtime.ToolCompleted, time.Unix(106, 0).UTC()
	completed.Data.Effect, completed.Data.Text = runtime.NoEffect, "observed content"
	events = append(events, started, completed)
	compact := base[3]
	compact.Sequence, compact.Time = 6, time.Unix(107, 0).UTC()
	events = append(events, compact)
	plan, err := PlanInterruptedReadOnlyModel([][]runtime.Event{events}, time.Unix(1000, 0).UTC())
	if err != nil || len(plan.Events) != 1 || plan.Events[0].Kind != runtime.TaskFailed || plan.Events[0].Data.Code != "interrupted_read_only_model" || plan.Events[0].CausationID != compact.ID || plan.Events[0].TurnID != "" || plan.Events[0].AttemptID != "" {
		t.Fatal("read-only compaction boundary was not safely terminalized", plan, err)
	}
}

type snapshotEventsForTest []runtime.Event

func (events snapshotEventsForTest) Read(_ context.Context, _ string, after int64, limit int) ([]runtime.Event, error) {
	end := min(int64(len(events)), after+int64(limit))
	return events[after:end], nil
}
