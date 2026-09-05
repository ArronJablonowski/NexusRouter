package sessions

import (
	"bytes"
	"encoding/json"
	"io"
	"unicode/utf8"
)

// ParseSummaryDraft validates the bounded, versioned generated-summary format.
// Structural validity does not establish accuracy or authorize compaction.
// Parsing is local to this adapter; SDK JSON decoding is unchanged.
func ParseSummaryDraft(raw []byte) (Summary, error) {
	if len(raw) > 64<<10 || !utf8.Valid(raw) {
		return Summary{}, ErrHistory
	}
	envelope, err := summaryDraftObject(raw)
	if err != nil || len(envelope) != 2 || !bytes.Equal(bytes.TrimSpace(envelope["version"]), []byte("1")) {
		return Summary{}, ErrHistory
	}
	fields, err := summaryDraftObject(envelope["summary"])
	if err != nil {
		return Summary{}, ErrHistory
	}
	var summary Summary
	for key, rawItems := range fields {
		var target *[]string
		switch key {
		case "decisions":
			target = &summary.Decisions
		case "pending_work":
			target = &summary.PendingWork
		case "failures":
			target = &summary.Failures
		case "artifacts":
			target = &summary.Artifacts
		case "requirements":
			target = &summary.Requirements
		case "activity":
			target = &summary.Activity
		default:
			return Summary{}, ErrHistory
		}
		items := bytes.TrimSpace(rawItems)
		if len(items) == 0 || items[0] != '[' {
			return Summary{}, ErrHistory
		}
		var values []json.RawMessage
		if json.Unmarshal(items, &values) != nil {
			return Summary{}, ErrHistory
		}
		*target = make([]string, len(values))
		for i, value := range values {
			value = bytes.TrimSpace(value)
			if len(value) == 0 || value[0] != '"' || json.Unmarshal(value, &(*target)[i]) != nil {
				return Summary{}, ErrHistory
			}
		}
	}
	if ValidateCompactionRequest(&CompactionRequest{Keep: 1, Summary: summary}) != nil {
		return Summary{}, ErrHistory
	}
	return summary, nil
}

// Token-level keys reject duplicates (including escaped spellings) before a
// map can silently overwrite them. Callers validate exact allowed key names.
func summaryDraftObject(raw []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, ErrHistory
	}
	result := map[string]json.RawMessage{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, ErrHistory
		}
		key, ok := token.(string)
		if !ok {
			return nil, ErrHistory
		}
		if _, duplicate := result[key]; duplicate {
			return nil, ErrHistory
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, ErrHistory
		}
		result[key] = value
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, ErrHistory
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, ErrHistory
	}
	return result, nil
}
