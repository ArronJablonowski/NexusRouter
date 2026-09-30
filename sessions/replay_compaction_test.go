package sessions

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func TestReplayRetainsDetachedCompaction(t *testing.T) {
	events := fixture()
	events[0].Data.ParentTaskID = "parent"
	summary := runtime.ContextSummary{Requirements: []string{"Keep local-only"}, Activity: []string{"Inspected report.txt"}, Decisions: []string{"Decision"}, PendingWork: []string{"Pending"}, Failures: []string{"Failed test"}, Artifacts: []string{"report.txt"}}
	events[0].Data.Compaction = &runtime.ContextCompaction{Version: 1, SourceTaskID: "parent", SourceSequence: 42, SourceDigest: strings.Repeat("0", 64), RemovedMessages: 5, Summary: summary}
	s, err := Replay(context.Background(), events, "task")
	if err != nil || s.Compaction == nil || s.Compaction.SourceSequence != 42 || !reflect.DeepEqual(s.Compaction.Summary, summary) || len(s.Messages) != 3 || s.Messages[1].ToolCalls[0].ID != "call" || s.Messages[2].ToolCallID != "call" {
		t.Fatal(s, err)
	}
	s.Compaction.Summary.Decisions[0] = "mutated"
	if events[0].Data.Compaction.Summary.Decisions[0] != "Decision" {
		t.Fatal("snapshot aliases persisted metadata")
	}
	body, err := json.Marshal(s)
	if err != nil || !strings.Contains(string(body), `"source_sequence":42`) || !strings.Contains(string(body), `"requirements":["Keep local-only"]`) || !strings.Contains(string(body), `"activity":["Inspected report.txt"]`) || !strings.Contains(string(body), `"pending_work":["Pending"]`) {
		t.Fatal("inspection omitted metadata", string(body), err)
	}
	// A legacy source without compaction continues to replay unchanged.
	legacy, err := Replay(context.Background(), fixture(), "task")
	if err != nil || legacy.Compaction != nil {
		t.Fatal(legacy, err)
	}
}

func TestReplayRejectsCompactionSourceMismatch(t *testing.T) {
	events := fixture()
	events[0].Data.ParentTaskID = "parent"
	events[0].Data.Compaction = &runtime.ContextCompaction{Version: 1, SourceTaskID: "forged", SourceSequence: 1, SourceDigest: strings.Repeat("0", 64), RemovedMessages: 1, Summary: runtime.ContextSummary{Decisions: []string{"untrusted"}}}
	if _, err := Replay(context.Background(), events, "task"); err == nil {
		t.Fatal("mismatched source accepted")
	}
}
