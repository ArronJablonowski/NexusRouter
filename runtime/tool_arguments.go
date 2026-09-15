package runtime

import (
	"bytes"
	"encoding/json"
	"io"
)

// canonicalToolArguments accepts exactly one JSON object, rejects duplicate
// members at every depth, preserves JSON number precision, and returns the one
// byte representation used by both the durable journal and tool authorization.
func canonicalToolArguments(raw json.RawMessage) (json.RawMessage, error) {
	if err := rejectDuplicateToolArgumentMembers(raw); err != nil {
		return nil, ErrProtocol
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value map[string]any
	if decoder.Decode(&value) != nil || value == nil || decoder.Decode(new(any)) != io.EOF {
		return nil, ErrProtocol
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, ErrProtocol
	}
	return json.RawMessage(canonical), nil
}

func rejectDuplicateToolArgumentMembers(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if rejectDuplicateToolArgumentValue(decoder) != nil {
		return ErrProtocol
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ErrProtocol
	}
	return nil
}

func rejectDuplicateToolArgumentValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return ErrProtocol
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, keyErr := decoder.Token()
			key, stringKey := keyToken.(string)
			if keyErr != nil || !stringKey {
				return ErrProtocol
			}
			if _, duplicate := seen[key]; duplicate {
				return ErrProtocol
			}
			seen[key] = struct{}{}
			if rejectDuplicateToolArgumentValue(decoder) != nil {
				return ErrProtocol
			}
		}
		end, endErr := decoder.Token()
		if endErr != nil || end != json.Delim('}') {
			return ErrProtocol
		}
	case '[':
		for decoder.More() {
			if rejectDuplicateToolArgumentValue(decoder) != nil {
				return ErrProtocol
			}
		}
		end, endErr := decoder.Token()
		if endErr != nil || end != json.Delim(']') {
			return ErrProtocol
		}
	default:
		return ErrProtocol
	}
	return nil
}
