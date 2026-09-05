package codexbridge

import (
	"encoding/json"
	"strings"
	"unicode"

	"github.com/ArronJablonowski/DarwinRouter/internal/codexrpc"
	"github.com/ArronJablonowski/DarwinRouter/providers"
)

// ValidateInitialMessages checks whether a complete conversation can be
// projected into the pinned app-server history protocol. It grants no tools or
// permissions and does not require historical tools in the active catalog.
func ValidateInitialMessages(messages []providers.Message) error {
	_, _, err := initialHistory(messages)
	return err
}

// initialHistory constructs only the three supported typed ResponseItem forms.
// Serialized items own their bytes; no caller-owned argument buffer is retained.
func initialHistory(messages []providers.Message) ([]json.RawMessage, string, error) {
	if len(messages) == 0 || !validRequest(providers.Request{Messages: messages}) || providers.ValidateMessages(messages) != nil {
		return nil, "", failure(false)
	}
	last := messages[len(messages)-1]
	if last.Role != "user" || len(last.ToolCalls) != 0 || last.ToolCallID != "" {
		return nil, "", failure(false)
	}
	var items []json.RawMessage
	appendItem := func(value any) {
		// Every value below consists exclusively of strings and slices of them.
		body, _ := json.Marshal(value)
		items = append(items, body)
	}
	for _, message := range messages[:len(messages)-1] {
		switch message.Role {
		case "system", "user", "assistant":
			if message.Role != "assistant" || message.Content != "" || len(message.ToolCalls) == 0 {
				contentType := "input_text"
				if message.Role == "assistant" {
					contentType = "output_text"
				}
				appendItem(map[string]any{"type": "message", "role": message.Role, "content": []map[string]string{{"type": contentType, "text": message.Content}}})
			}
			for _, call := range message.ToolCalls {
				if !namePattern.MatchString(call.Name) || strings.TrimSpace(call.ID) == "" || len(call.ID) > 256 || strings.IndexFunc(call.ID, unicode.IsControl) >= 0 {
					return nil, "", failure(false)
				}
				appendItem(map[string]string{"type": "function_call", "namespace": "darwin", "name": call.Name, "call_id": call.ID, "arguments": string(call.Arguments)})
			}
		case "tool":
			appendItem(map[string]string{"type": "function_call_output", "call_id": message.ToolCallID, "output": providers.ToolResultContent(message)})
		}
	}
	if len(items) > 0 {
		// Thread IDs are bounded to 256 bytes by the session. '<' has maximal
		// JSON escaping expansion per input byte (six), so this also covers
		// escaped valid opaque IDs. Include the complete injection envelope.
		params, _ := json.Marshal(map[string]any{"threadId": strings.Repeat("<", 256), "items": items})
		frame, _ := json.Marshal(codexrpc.Envelope{ID: json.RawMessage("4"), Method: "thread/inject_items", Params: params})
		if len(frame) > codexrpc.DefaultMaxFrame {
			return nil, "", failure(false)
		}
	}
	return items, last.Content, nil
}
