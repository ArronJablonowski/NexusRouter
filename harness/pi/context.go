package pi

import (
	"encoding/json"

	"github.com/ArronJablonowski/NexusRouter/providers"
)

// contextMessages encodes host-assembled roles exactly for the single native
// completion. Tool-bearing history requires a separately supported capability.
func contextMessages(messages []providers.Message) ([]byte, error) {
	if len(messages) == 0 {
		return nil, nil
	}
	if providers.ValidateMessages(messages) != nil {
		return nil, ErrProtocol
	}
	type message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	wire := make([]message, 0, len(messages))
	for _, m := range messages {
		if len(m.ToolCalls) != 0 || m.ToolCallID != "" || m.ToolFailed || (m.Role != "system" && m.Role != "user" && m.Role != "assistant") {
			return nil, ErrProtocol
		}
		wire = append(wire, message{m.Role, m.Content})
	}
	body, err := json.Marshal(wire)
	if err != nil || len(body) > MaxRecordBytes/2 {
		return nil, ErrProtocol
	}
	return body, nil
}
