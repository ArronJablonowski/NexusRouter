package diagnostics

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
)

// EncodeLine applies current configured-credential redaction once more at the
// local export boundary, including JSON keys and structured tool arguments.
// The source journal is never rewritten. No raw errors are returned to logs.
func EncodeLine(record Record, secrets []string) ([]byte, error) {
	body, err := json.Marshal(record)
	if err != nil || len(body) > MaxRecordBytes {
		return nil, ErrDiagnostic
	}
	values := make([]string, 0, len(secrets))
	for _, secret := range secrets {
		if secret != "" {
			values = append(values, secret)
		}
	}
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	if len(values) > 0 {
		var object any
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.UseNumber()
		if decoder.Decode(&object) != nil {
			return nil, ErrDiagnostic
		}
		redacted, redactErr := redact(object, values)
		if redactErr != nil {
			return nil, ErrDiagnostic
		}
		body, err = json.Marshal(redacted)
		if err != nil || len(body) > MaxRecordBytes {
			return nil, ErrDiagnostic
		}
	}
	return append(body, '\n'), nil
}

func redact(value any, secrets []string) (any, error) {
	switch item := value.(type) {
	case string:
		for _, secret := range secrets {
			item = strings.ReplaceAll(item, secret, "[REDACTED]")
		}
		return item, nil
	case []any:
		for i := range item {
			clean, err := redact(item[i], secrets)
			if err != nil {
				return nil, err
			}
			item[i] = clean
		}
	case map[string]any:
		out := make(map[string]any, len(item))
		for key, member := range item {
			cleanKey, err := redact(key, secrets)
			if err != nil {
				return nil, err
			}
			if _, exists := out[cleanKey.(string)]; exists {
				return nil, ErrDiagnostic
			}
			cleanValue, err := redact(member, secrets)
			if err != nil {
				return nil, err
			}
			out[cleanKey.(string)] = cleanValue
		}
		return out, nil
	}
	return value, nil
}
