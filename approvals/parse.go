package approvals

import (
	"bytes"
	"encoding/json"
	"io"
	"unicode/utf8"
)

// ParseCommand accepts one strict, bounded JSON command. Duplicate/unknown or
// case-aliased keys and null values fail closed, including Expected bindings.
func ParseCommand(body []byte) (Command, error) {
	if len(body) > 16<<10 || !utf8.Valid(body) {
		return Command{}, ErrInvalid
	}
	fields, err := commandObject(body, "expected", "id", "allowed")
	if err != nil {
		return Command{}, err
	}
	if _, err = commandObject(fields["expected"], "version", "id", "task_id", "turn_id", "tool_call_id", "tool_name", "scope", "arguments_digest", "schema_digest", "policy_digest", "created_at", "expires_at"); err != nil {
		return Command{}, err
	}
	var c Command
	if json.Unmarshal(body, &c) != nil || c.Validate() != nil {
		return Command{}, ErrInvalid
	}
	return c, nil
}

func commandObject(body []byte, keys ...string) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(body))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, ErrInvalid
	}
	fields := map[string]json.RawMessage{}
	for d.More() {
		token, err = d.Token()
		key, ok := token.(string)
		if err != nil || !ok || fields[key] != nil {
			return nil, ErrInvalid
		}
		known := false
		for _, allowed := range keys {
			if key == allowed {
				known = true
				break
			}
		}
		if !known {
			return nil, ErrInvalid
		}
		var raw json.RawMessage
		if d.Decode(&raw) != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return nil, ErrInvalid
		}
		fields[key] = raw
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') || len(fields) != len(keys) {
		return nil, ErrInvalid
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, ErrInvalid
	}
	return fields, nil
}
