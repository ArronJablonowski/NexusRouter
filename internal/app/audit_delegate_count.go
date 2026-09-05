package app

import (
	"bytes"
	"encoding/json"
	"io"
	"unicode/utf8"
)

// Validate the paired argument shape while retaining only cardinality. JSON
// key order is immaterial for model-authored arguments; aliases, duplicate keys
// and unknown fields are not. This does not prove which input a child executed.
func auditBatchTaskCount(raw []byte) (int, error) {
	if len(raw) > 512<<10 || !utf8.Valid(raw) {
		return 0, ErrAdmission
	}
	fields, err := auditBatchObject(raw, "tasks")
	if err != nil {
		return 0, err
	}
	var tasks []json.RawMessage
	if json.Unmarshal(fields["tasks"], &tasks) != nil || len(tasks) < 2 || len(tasks) > 4 {
		return 0, ErrAdmission
	}
	for _, task := range tasks {
		fields, err := auditBatchObject(task, "prompt", "validation")
		if err != nil {
			return 0, err
		}
		var input delegateInput
		if json.Unmarshal(fields["prompt"], &input.Prompt) != nil || json.Unmarshal(fields["validation"], &input.Validation) != nil || !input.valid() {
			return 0, ErrAdmission
		}
	}
	return len(tasks), nil
}

func auditBatchObject(raw []byte, names ...string) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, ErrAdmission
	}
	fields := make(map[string]json.RawMessage, len(names))
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		if err != nil || !ok || fields[key] != nil {
			return nil, ErrAdmission
		}
		allowed := false
		for _, name := range names {
			if key == name {
				allowed = true
			}
		}
		if !allowed {
			return nil, ErrAdmission
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil, ErrAdmission
		}
		fields[key] = value
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') || len(fields) != len(names) {
		return nil, ErrAdmission
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, ErrAdmission
	}
	return fields, nil
}

func auditBatchResultCount(text string) (int, error) {
	if len(text) >= 1<<20 || !utf8.ValidString(text) {
		return 0, ErrAdmission
	}
	body := bytes.TrimSpace([]byte(text))
	if bytes.Equal(body, []byte(`{"error":"delegate_unavailable_or_rejected"}`)) {
		return 0, nil
	}
	var batch struct {
		Results []json.RawMessage `json:"results"`
	}
	if json.Unmarshal(body, &batch) != nil || len(batch.Results) < 2 || len(batch.Results) > 4 {
		return 0, ErrAdmission
	}
	// The traversal parser subsequently validates the canonical outer and leaf
	// shapes. This count includes generic failures, not only traversed children.
	return len(batch.Results), nil
}
