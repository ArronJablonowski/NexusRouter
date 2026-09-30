package sessions

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/providers"
)

func TestToolFailureSurvivesReplayCompactionAndSerialization(t *testing.T) {
	for _, failed := range []bool{false, true} {
		events := fixture()
		if failed {
			events[5].Data.Code = "tool_failed"
			events[5].Data.Text = `{"error":"file_unavailable"}`
		}
		snapshot, err := Replay(context.Background(), events, "task")
		if err != nil || snapshot.State != "completed" || len(snapshot.Messages) != 3 {
			t.Fatal("paired history unavailable", err)
		}
		result := snapshot.Messages[2]
		if result.ToolFailed != failed || result.ToolCallID != "call" || result.Content != events[5].Data.Text {
			t.Fatal("tool result changed on replay", result)
		}
		if err := providers.ValidateMessages(snapshot.Messages); err != nil {
			t.Fatal("replayed failure broke pairing", err)
		}
		// A cut at a failed result must retain the paired assistant proposal and
		// preserve status, rather than turning the result into a success string.
		compacted, err := Compact(snapshot.Messages, 1, Summary{Failures: []string{"Observed tool outcomes retained below"}})
		if err != nil || len(compacted.Recent) != 2 || compacted.Recent[1].ToolFailed != failed {
			t.Fatal("compaction lost failure or pairing", err)
		}
		body, err := json.Marshal(compacted)
		if err != nil {
			t.Fatal(err)
		}
		var roundtrip Compaction
		if err := json.Unmarshal(body, &roundtrip); err != nil || !reflect.DeepEqual(roundtrip, compacted) {
			t.Fatal("failure metadata lost during serialization", err)
		}
		// Reader-owned event edits cannot change the returned failure snapshot.
		events[5].Data.Code = "changed"
		if snapshot.Messages[2].ToolFailed != failed {
			t.Fatal("reader mutated replayed result")
		}
	}
}
