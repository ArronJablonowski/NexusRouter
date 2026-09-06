package app

import (
	"bytes"
	"encoding/json"
	"io"
	"unicode/utf8"

	"github.com/ArronJablonowski/DarwinRouter/providers"
)

// Workflow source steps are host-serialized messages, not arbitrary model
// instructions. Decode originals before redaction so JSON escaping cannot hide
// credentials or literal replacement corrupt structured tool observations.
func redactCodexWorkflowSteps(steps []string, secrets []string) ([]string, error) {
	if len(steps) == 0 || len(steps) > 128 {
		return nil, ErrAdmission
	}
	messages := make([]providers.Message, 0, len(steps))
	for _, step := range steps {
		if len(step) > 1<<20 || !utf8.ValidString(step) {
			return nil, ErrAdmission
		}
		decoder := json.NewDecoder(bytes.NewBufferString(step))
		decoder.UseNumber()
		value, err := codexHistoryJSON(decoder, 0)
		if err != nil || !canonicalWorkflowMessageFields(value) {
			return nil, ErrAdmission
		}
		if _, err := decoder.Token(); err != io.EOF {
			return nil, ErrAdmission
		}
		body, err := json.Marshal(value)
		if err != nil {
			return nil, ErrAdmission
		}
		decoder = json.NewDecoder(bytes.NewReader(body))
		decoder.DisallowUnknownFields()
		var message providers.Message
		if decoder.Decode(&message) != nil {
			return nil, ErrAdmission
		}
		messages = append(messages, message)
	}
	clean, err := redactCodexHistoryMessages(messages, secrets)
	if err != nil {
		return nil, ErrAdmission
	}
	out := make([]string, len(clean))
	for i, message := range clean {
		body, err := json.Marshal(message)
		if err != nil {
			return nil, ErrAdmission
		}
		out[i] = string(body)
	}
	return out, nil
}

// encoding/json accepts case-insensitive struct field aliases even with
// DisallowUnknownFields. These sources are host-serialized Message objects:
// enforce their exact public field spellings before the typed decode. Tool
// argument keys are task data and are deliberately not treated as struct keys.
func canonicalWorkflowMessageFields(value any) bool {
	message, ok := value.(map[string]any)
	if !ok {
		return false
	}
	if _, ok := message["role"].(string); !ok {
		return false
	}
	if _, ok := message["content"].(string); !ok {
		return false
	}
	for key, field := range message {
		switch key {
		case "role", "content":
		case "tool_call_id":
			if _, ok := field.(string); !ok {
				return false
			}
		case "tool_failed":
			if _, ok := field.(bool); !ok {
				return false
			}
		case "tool_calls":
			calls, ok := field.([]any)
			if !ok {
				return false
			}
			for _, value := range calls {
				call, ok := value.(map[string]any)
				if !ok || len(call) != 3 {
					return false
				}
				if _, ok := call["id"].(string); !ok {
					return false
				}
				if _, ok := call["name"].(string); !ok {
					return false
				}
				if _, ok := call["arguments"].(map[string]any); !ok {
					return false
				}
			}
		default:
			return false
		}
	}
	return true
}
