package sessions

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func TestMultiEpochReplayPreservesRetiredToolIdentity(t *testing.T) {
	events := contextCompactionReplayEvents()[:4]
	retired := providers.ToolCall{ID: "retired-call", Name: "lookup", Arguments: json.RawMessage(`{}`)}
	events[0].Data.Messages = []providers.Message{
		{Role: "assistant", ToolCalls: []providers.ToolCall{retired}},
		{Role: "tool", ToolCallID: retired.ID, Content: "old result"},
		{Role: "user", Content: "current question"},
	}
	events[3].Data.Compaction.FirstRetainedMessage = events[3].Data.Compaction.RemovedMessages
	lineage, err := runtime.ExtendContextLineage(nil, "task", 4, events[3].Data.Compaction, events[0].Data.Messages)
	if err != nil {
		t.Fatal(err)
	}
	events[3].Data.ContextLineage = lineage
	expectedLineage, expectedErr := runtime.ExtendContextLineage(nil, "task", 4, events[3].Data.Compaction, events[0].Data.Messages)
	if expectedErr != nil || expectedLineage.Digest != lineage.Digest {
		t.Fatal("fixture lineage mismatch", expectedErr)
	}
	terminal := events[0]
	terminal.ID, terminal.Sequence, terminal.Kind, terminal.Time = "complete", 5, runtime.TaskCompleted, time.Unix(110, 0).UTC()
	terminal.Data = runtime.Data{}
	events = append(events, terminal)
	for _, event := range events {
		if validateErr := event.Validate(); validateErr != nil {
			t.Fatalf("invalid fixture event %s: %v", event.ID, validateErr)
		}
	}
	source, err := Replay(context.Background(), snapshotEventsForTest(events), "task")
	if err != nil || source.ContextLineage == nil || len(source.ContextLineage.Epochs) != 1 {
		t.Fatalf("replay sequence=%d state=%q messages=%d lineage=%v err=%v", source.Sequence, source.State, len(source.Messages), source.ContextLineage, err)
	}
	messages, checkpoint, err := PrepareContinuation(source, CompactionRequest{Keep: 1, Summary: Summary{Decisions: []string{"next epoch"}}})
	if err != nil || checkpoint.Version != 2 || checkpoint.SourceStateDigest == "" || len(checkpoint.SourceToolCallIDs) != 1 || checkpoint.SourceToolCallIDs[0] != retired.ID {
		t.Fatal(checkpoint, err)
	}
	warnings := 0
	for _, message := range messages {
		if strings.Contains(message.Content, "operator-supplied summary of earlier conversation") {
			warnings++
		}
	}
	if warnings != 1 {
		t.Fatalf("summary scaffolding stacked across epochs: %d", warnings)
	}
	childLineage, err := runtime.ExtendContextLineage(source.ContextLineage, "child", 1, checkpoint, messages)
	if err != nil {
		t.Fatal(err)
	}
	start := runtime.Event{Version: 1, ID: "child-start", TaskID: "child", SessionID: "session", CorrelationID: "child", Sequence: 1, Time: time.Unix(120, 0).UTC(), Kind: runtime.TaskStarted, Data: runtime.Data{ParentTaskID: "task", Compaction: checkpoint, ContextLineage: childLineage, Messages: messages}}
	turn := runtime.Event{Version: 1, ID: "child-turn", TaskID: "child", SessionID: "session", CorrelationID: "child", Sequence: 2, Time: time.Unix(121, 0).UTC(), Kind: runtime.TurnStarted, TurnID: "turn", AttemptID: "attempt"}
	answer := runtime.Event{Version: 1, ID: "child-answer", TaskID: "child", SessionID: "session", CorrelationID: "child", Sequence: 3, Time: time.Unix(122, 0).UTC(), Kind: runtime.TurnCompleted, TurnID: "turn", AttemptID: "attempt", Data: runtime.Data{Text: "answer", FinishReason: "stop"}}
	childTerminal := runtime.Event{Version: 1, ID: "child-complete", TaskID: "child", SessionID: "session", CorrelationID: "child", Sequence: 4, Time: time.Unix(123, 0).UTC(), Kind: runtime.TaskCompleted}
	twiceCompacted, err := Replay(context.Background(), snapshotEventsForTest([]runtime.Event{start, turn, answer, childTerminal}), "child")
	if err != nil || twiceCompacted.ContextLineage == nil || len(twiceCompacted.ContextLineage.Epochs) != 2 {
		t.Fatal("twice-compacted replay failed", err)
	}
	if _, third, thirdErr := PrepareContinuation(twiceCompacted, CompactionRequest{Keep: 1, Summary: Summary{PendingWork: []string{"third epoch"}}}); thirdErr != nil || third.Version != 2 || third.SourceStateDigest == "" {
		t.Fatal("third epoch preparation failed", thirdErr)
	}
	done := runtime.Event{Version: 1, ID: "child-done", TaskID: "child", SessionID: "session", CorrelationID: "child", Sequence: 3, Time: time.Unix(122, 0).UTC(), Kind: runtime.TurnCompleted, TurnID: "turn", AttemptID: "attempt", Data: runtime.Data{ToolCalls: []providers.ToolCall{retired}, FinishReason: "tool_calls"}}
	if _, err := Replay(context.Background(), snapshotEventsForTest([]runtime.Event{start, turn, done}), "child"); err == nil {
		t.Fatal("retired cross-epoch tool identity was reused")
	}
}

func TestReplayRejectsRewrittenCompactionLineage(t *testing.T) {
	events := contextCompactionReplayEvents()[:4]
	lineage, err := runtime.ExtendContextLineage(nil, "task", 4, events[3].Data.Compaction, events[0].Data.Messages)
	if err != nil {
		t.Fatal(err)
	}
	events[3].Data.ContextLineage = lineage
	lineage.Epochs[0].ActivationSequence = 3
	if _, err := Replay(context.Background(), snapshotEventsForTest(events), "task"); err == nil {
		t.Fatal("rewritten lineage accepted")
	}
}

func TestReplayRejectsVersionTwoCompactionWithoutLineage(t *testing.T) {
	for name, events := range map[string][]runtime.Event{
		"task start":       contextCompactionReplayEvents()[:1],
		"activation event": contextCompactionReplayEvents()[:4],
	} {
		t.Run(name, func(t *testing.T) {
			index := len(events) - 1
			if name == "task start" {
				events[0].Data.ParentTaskID = "source"
				events[0].Data.Compaction = eventsForVersionTwoCheckpoint()
			} else {
				events[index].Data.Compaction = eventsForVersionTwoCheckpoint()
			}
			if _, err := Replay(context.Background(), snapshotEventsForTest(events), "task"); err == nil {
				t.Fatal("version-two compaction replayed without lineage")
			}
		})
	}
}

func eventsForVersionTwoCheckpoint() *runtime.ContextCompaction {
	return &runtime.ContextCompaction{Version: 2, SummaryAttemptID: "summary", SummaryReviewID: "review", SourceTaskID: "source", SourceSequence: 4,
		SourceDigest: strings.Repeat("a", 64), SourceStateDigest: strings.Repeat("b", 64), RemovedMessages: 1, Summary: runtime.ContextSummary{Decisions: []string{"retain"}}}
}

func TestReplayRejectsSelfConsistentForkedPriorEpoch(t *testing.T) {
	first := &runtime.ContextCompaction{Version: 1, SourceTaskID: "original", SourceSequence: 2, SourceDigest: strings.Repeat("a", 64), RemovedMessages: 1, Summary: runtime.ContextSummary{Decisions: []string{"first"}}}
	inherited, err := runtime.ExtendContextLineage(nil, "source", 1, first, []providers.Message{{Role: "user", Content: "old"}})
	if err != nil {
		t.Fatal(err)
	}
	start := runtime.Event{Version: 1, ID: "start", TaskID: "task", SessionID: "session", CorrelationID: "task", Sequence: 1, Time: time.Unix(200, 0).UTC(), Kind: runtime.TaskStarted,
		Data: runtime.Data{ParentTaskID: "source", ContextLineage: inherited, Messages: []providers.Message{{Role: "user", Content: "current"}}}}
	turn := runtime.Event{Version: 1, ID: "turn", TaskID: "task", SessionID: "session", CorrelationID: "task", Sequence: 2, Time: time.Unix(201, 0).UTC(), Kind: runtime.TurnStarted, TurnID: "turn", AttemptID: "attempt"}
	done := runtime.Event{Version: 1, ID: "done", TaskID: "task", SessionID: "session", CorrelationID: "task", Sequence: 3, Time: time.Unix(202, 0).UTC(), Kind: runtime.TurnCompleted, TurnID: "turn", AttemptID: "attempt", Data: runtime.Data{Text: "answer", FinishReason: "stop"}}
	checkpoint := eventsForVersionTwoCheckpoint()
	lineage, err := runtime.ExtendContextLineage(inherited, "task", 4, checkpoint, start.Data.Messages)
	if err != nil {
		t.Fatal(err)
	}
	lineage.Epochs[0].TaskID = "self-consistent-fork"
	lineage.Digest, err = lineage.CanonicalDigest()
	if err != nil || lineage.Validate() != nil {
		t.Fatal("could not construct self-consistent fork", err)
	}
	compact := runtime.Event{Version: 1, ID: "compact", TaskID: "task", SessionID: "session", CorrelationID: "task", Sequence: 4, Time: time.Unix(203, 0).UTC(), Kind: runtime.ContextCompacted,
		Data: runtime.Data{ParentTaskID: "source", Compaction: checkpoint, ContextLineage: lineage, Messages: []providers.Message{{Role: "user", Content: "replacement"}}, ReplacedMessages: 1}}
	if compact.Validate() != nil {
		t.Fatal("fixture must be envelope-valid")
	}
	if _, err := Replay(context.Background(), snapshotEventsForTest([]runtime.Event{start, turn, done, compact}), "task"); err == nil {
		t.Fatal("self-consistent forked prior epoch replayed")
	}
}
