package app

import (
	"bytes"
	"encoding/json"

	"darwinrouter/providers"
	"darwinrouter/sessions"
)

func redactSummaryMessages(messages []providers.Message, secrets []string) ([]providers.Message, error) {
	copy, err := sessions.Compact(messages, len(messages), sessions.Summary{})
	if err != nil {
		return nil, ErrAdmission
	}
	for i := range copy.Recent {
		m := &copy.Recent[i]
		m.Content = redact(m.Content, secrets)
		m.ToolCallID = redact(m.ToolCallID, secrets)
		for j := range m.ToolCalls {
			call := &m.ToolCalls[j]
			call.ID, call.Name = redact(call.ID, secrets), redact(call.Name, secrets)
			// Preserve numeric precision while scrubbing both JSON string values
			// and keys. Redacted-key collisions are rejected rather than hiding
			// observed arguments or accidentally disclosing their original keys.
			decoder := json.NewDecoder(bytes.NewReader(call.Arguments))
			decoder.UseNumber()
			var value any
			if decoder.Decode(&value) != nil {
				return nil, ErrAdmission
			}
			value, err = redactSummaryJSON(value, secrets)
			if err != nil {
				return nil, err
			}
			call.Arguments, err = json.Marshal(value)
			if err != nil {
				return nil, ErrAdmission
			}
		}
	}
	if providers.ValidateMessages(copy.Recent) != nil {
		return nil, ErrAdmission
	}
	return copy.Recent, nil
}

func redactSummaryJSON(value any, secrets []string) (any, error) {
	switch v := value.(type) {
	case string:
		return redact(v, secrets), nil
	case []any:
		for i := range v {
			clean, err := redactSummaryJSON(v[i], secrets)
			if err != nil {
				return nil, err
			}
			v[i] = clean
		}
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			key = redact(key, secrets)
			if _, exists := out[key]; exists {
				return nil, ErrAdmission
			}
			clean, err := redactSummaryJSON(item, secrets)
			if err != nil {
				return nil, err
			}
			out[key] = clean
		}
		return out, nil
	}
	return value, nil
}
