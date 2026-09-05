package skills

import (
	"bytes"
	"encoding/json"
	"io"
	"slices"
	"unicode/utf8"
)

// parseModelDraft accepts only workflow content; identity and provenance are
// supplied by the host. The proposal is never evidence of successful validation.
func parseModelDraft(raw []byte, key Key, sessions, evidence []string) (Draft, error) {
	if len(raw) == 0 || len(raw) > 64<<10 || !utf8.Valid(raw) {
		return Draft{}, ErrValidation
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return Draft{}, ErrValidation
	}
	draft := Draft{Key: key, SourceSessions: slices.Clone(sessions), SourceEvidence: slices.Clone(evidence)}
	seen := map[string]bool{}
	for decoder.More() {
		token, err = decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok || seen[name] {
			return Draft{}, ErrValidation
		}
		seen[name] = true
		var value json.RawMessage
		if decoder.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return Draft{}, ErrValidation
		}
		var target any
		switch name {
		case "version":
			var version int
			if json.Unmarshal(value, &version) != nil || version != 1 {
				return Draft{}, ErrValidation
			}
			continue
		case "description":
			target = &draft.Description
		case "tags":
			target = &draft.Tags
		case "steps":
			target = &draft.Steps
		case "required_tools":
			target = &draft.RequiredTools
		case "configuration":
			target = &draft.Configuration
		case "risks":
			target = &draft.Risks
		case "validation_cases":
			target = &draft.ValidationCases
		default:
			return Draft{}, ErrValidation
		}
		if json.Unmarshal(value, target) != nil {
			return Draft{}, ErrValidation
		}
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') || len(seen) != 8 {
		return Draft{}, ErrValidation
	}
	if _, err = decoder.Token(); err != io.EOF || validateGeneratedDraft(draft) != nil {
		return Draft{}, ErrValidation
	}
	return draft, nil
}
