package sessions

import (
	"encoding/json"
	"errors"

	"github.com/ArronJablonowski/NexusRouter/providers"
)

const (
	MaxTranscriptMessages = 512
	MaxTranscriptBytes    = 1 << 20
)

var ErrTranscript = errors.New("transcript unavailable or invalid")

// Transcript is the bounded, committed conversational projection of a task.
// Incomplete model output and incomplete tool batches are intentionally absent.
type Transcript struct {
	Version      int                 `json:"version"`
	TaskID       string              `json:"task_id"`
	SessionID    string              `json:"session_id"`
	State        string              `json:"state"`
	HeadSequence int64               `json:"head_sequence"`
	Messages     []providers.Message `json:"messages"`
}

// ProjectTranscript selects the longest structurally complete message prefix.
// Replay has already excluded provisional model deltas; this additional cut
// ensures an in-flight parallel tool batch is never exposed as orphaned calls
// or results.
func ProjectTranscript(snapshot Snapshot) (Transcript, error) {
	transcript := Transcript{Version: 1, TaskID: snapshot.TaskID, SessionID: snapshot.SessionID, State: snapshot.State, HeadSequence: snapshot.Sequence, Messages: []providers.Message{}}
	if !ValidEventPageID(snapshot.TaskID) || !ValidEventPageID(snapshot.SessionID) || !ValidTaskState(snapshot.State) || snapshot.Sequence < 1 || snapshot.Sequence > MaxTaskEvents || len(snapshot.Messages) != len(snapshot.MessageSequences) {
		return Transcript{}, ErrTranscript
	}
	complete := 0
	for index := 0; index < len(snapshot.Messages); {
		message := snapshot.Messages[index]
		if len(message.ToolCalls) == 0 {
			complete, index = index+1, index+1
			continue
		}
		pending := make(map[string]bool, len(message.ToolCalls))
		for _, call := range message.ToolCalls {
			pending[call.ID] = true
		}
		end := index + 1
		for end < len(snapshot.Messages) && snapshot.Messages[end].Role == "tool" {
			delete(pending, snapshot.Messages[end].ToolCallID)
			end++
		}
		if len(pending) != 0 {
			break
		}
		complete, index = end, end
	}
	transcript.Messages = cloneMessages(snapshot.Messages[:complete])
	if transcript.Validate() != nil {
		return Transcript{}, ErrTranscript
	}
	return transcript, nil
}

func (t Transcript) Validate() error {
	if t.Version != 1 || !ValidEventPageID(t.TaskID) || !ValidEventPageID(t.SessionID) || !ValidTaskState(t.State) || t.HeadSequence < 1 || t.HeadSequence > MaxTaskEvents || len(t.Messages) > MaxTranscriptMessages {
		return ErrTranscript
	}
	if len(t.Messages) > 0 && providers.ValidateMessages(t.Messages) != nil {
		return ErrTranscript
	}
	body, err := json.Marshal(t)
	if err != nil || len(body) > MaxTranscriptBytes {
		return ErrTranscript
	}
	return nil
}

func cloneMessages(messages []providers.Message) []providers.Message {
	if len(messages) == 0 {
		return []providers.Message{}
	}
	body, err := json.Marshal(messages)
	if err != nil {
		return nil
	}
	var clone []providers.Message
	if json.Unmarshal(body, &clone) != nil {
		return nil
	}
	return clone
}
