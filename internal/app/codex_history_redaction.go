package app

import (
	"encoding/json"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/ArronJablonowski/DarwinRouter/providers"
)

// Imported tool output can contain JSON-escaped credentials. Decode the
// original payload before redaction so an earlier literal replacement cannot
// corrupt its syntax or hide escaped secrets. This does not interpret output
// as instructions or grant any tool authority.
func redactCodexHistoryMessages(messages []providers.Message, secrets []string) ([]providers.Message, error) {
	clean, err := redactSummaryMessages(messages, secrets)
	if err != nil {
		return nil, ErrAdmission
	}
	for i, message := range messages {
		text := strings.TrimSpace(message.Content)
		if message.Role != "tool" || (!strings.HasPrefix(text, "{") && !strings.HasPrefix(text, "[")) {
			continue
		}
		if len(message.Content) > 1<<20 || !utf8.ValidString(message.Content) {
			return nil, ErrAdmission
		}
		decoder := json.NewDecoder(strings.NewReader(message.Content))
		decoder.UseNumber()
		value, err := codexHistoryJSON(decoder, 0)
		if err != nil {
			return nil, ErrAdmission
		}
		if _, err := decoder.Token(); err != io.EOF {
			return nil, ErrAdmission
		}
		value, err = redactSummaryJSON(value, secrets)
		if err != nil {
			return nil, ErrAdmission
		}
		body, err := json.Marshal(value)
		if err != nil {
			return nil, ErrAdmission
		}
		clean[i].Content = redact(string(body), secrets)
	}
	return clean, nil
}

// Token-based decoding retains numbers and rejects duplicate decoded keys.
// Depth counts containers, with the root object or array at depth one.
func codexHistoryJSON(decoder *json.Decoder, depth int) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, ErrAdmission
	}
	delim, container := token.(json.Delim)
	if !container {
		return token, nil
	}
	if depth >= 64 {
		return nil, ErrAdmission
	}
	switch delim {
	case '{':
		object := map[string]any{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			key, ok := keyToken.(string)
			if err != nil || !ok {
				return nil, ErrAdmission
			}
			if _, duplicate := object[key]; duplicate {
				return nil, ErrAdmission
			}
			value, err := codexHistoryJSON(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			object[key] = value
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return nil, ErrAdmission
		}
		return object, nil
	case '[':
		array := []any{}
		for decoder.More() {
			value, err := codexHistoryJSON(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return nil, ErrAdmission
		}
		return array, nil
	default:
		return nil, ErrAdmission
	}
}
