package sessions

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/providers"
)

func TestProjectTranscriptPreservesCompleteToolPairs(t *testing.T) {
	messages := []providers.Message{
		{Role: "user", Content: "question"},
		{Role: "assistant", ToolCalls: []providers.ToolCall{{ID: "a", Name: "read", Arguments: json.RawMessage(`{}`)}, {ID: "b", Name: "read", Arguments: json.RawMessage(`{}`)}}},
		{Role: "tool", ToolCallID: "a", Content: "one"},
		{Role: "tool", ToolCallID: "b", Content: "two"},
		{Role: "assistant", ToolCalls: []providers.ToolCall{{ID: "pending", Name: "read", Arguments: json.RawMessage(`{}`)}}},
	}
	snapshot := Snapshot{TaskID: "task", SessionID: "session", State: "running", Sequence: 9, Messages: messages, MessageSequences: []int64{1, 4, 6, 7, 9}}
	transcript, err := ProjectTranscript(snapshot)
	if err != nil || transcript.Validate() != nil || len(transcript.Messages) != 4 || transcript.Messages[1].ToolCalls[1].ID != "b" || transcript.Messages[3].ToolCallID != "b" {
		t.Fatal("complete tool batch was not preserved", transcript, err)
	}
	if transcript.Messages[0].Content = "changed"; snapshot.Messages[0].Content == "changed" {
		t.Fatal("transcript aliases replay state")
	}
}

func TestTranscriptBoundsFailClosed(t *testing.T) {
	base := Transcript{Version: 1, TaskID: "task", SessionID: "session", State: "completed", HeadSequence: 2, Messages: []providers.Message{{Role: "user", Content: "ok"}}}
	if base.Validate() != nil {
		t.Fatal("valid transcript rejected")
	}
	tooMany := base
	tooMany.Messages = make([]providers.Message, MaxTranscriptMessages+1)
	for index := range tooMany.Messages {
		tooMany.Messages[index] = providers.Message{Role: "user", Content: "x"}
	}
	tooLarge := base
	tooLarge.Messages = []providers.Message{{Role: "user", Content: strings.Repeat("x", MaxTranscriptBytes)}}
	for name, transcript := range map[string]Transcript{"messages": tooMany, "bytes": tooLarge} {
		if transcript.Validate() == nil {
			t.Fatal("unbounded transcript accepted", name)
		}
	}
}
