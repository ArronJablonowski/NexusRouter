package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"

	"github.com/ArronJablonowski/NexusRouter/sessions"
)

func decodeCompactionRequest(raw []byte) (*sessions.CompactionRequest, error) {
	bad := errors.New("invalid compaction request")
	fields, err := compactionObject(raw)
	if err != nil || len(fields) != 2 || fields["keep"] == nil || fields["summary"] == nil {
		return nil, bad
	}
	var request sessions.CompactionRequest
	if json.Unmarshal(fields["keep"], &request.Keep) != nil {
		return nil, bad
	}
	summary, err := compactionObject(fields["summary"])
	if err != nil {
		return nil, bad
	}
	for key, value := range summary {
		var target *[]string
		switch key {
		case "requirements":
			target = &request.Summary.Requirements
		case "activity":
			target = &request.Summary.Activity
		case "decisions":
			target = &request.Summary.Decisions
		case "pending_work":
			target = &request.Summary.PendingWork
		case "failures":
			target = &request.Summary.Failures
		case "artifacts":
			target = &request.Summary.Artifacts
		default:
			return nil, bad
		}
		var items []json.RawMessage
		if json.Unmarshal(value, &items) != nil || items == nil {
			return nil, bad
		}
		*target = make([]string, len(items))
		for i, item := range items {
			var text *string
			if json.Unmarshal(item, &text) != nil || text == nil {
				return nil, bad
			}
			(*target)[i] = *text
		}
	}
	if sessions.ValidateCompactionRequest(&request) != nil {
		return nil, bad
	}
	return &request, nil
}

// compactionObject retains raw values so nested duplicate keys cannot disappear
// into a map before their own schema is checked.
func compactionObject(raw []byte) (map[string]json.RawMessage, error) {
	bad := errors.New("invalid compaction object")
	if !utf8.Valid(raw) {
		return nil, bad
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	first, err := d.Token()
	if err != nil || first != json.Delim('{') {
		return nil, bad
	}
	fields := map[string]json.RawMessage{}
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		if err != nil || !ok || fields[key] != nil {
			return nil, bad
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil, bad
		}
		fields[key] = value
	}
	end, err := d.Token()
	if err != nil || end != json.Delim('}') {
		return nil, bad
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, bad
	}
	return fields, nil
}
