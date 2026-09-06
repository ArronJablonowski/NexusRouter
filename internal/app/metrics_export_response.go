package app

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
	"strconv"
)

var metricsExportZero = regexp.MustCompile(`^-?0(?:\.0+)?(?:[eE][+-]?[0-9]+)?$`)

// OTLP requires unknown response fields to be ignored for future compatibility.
// Known duplicate keys are ambiguous and rejected. Diagnostic messages are never
// returned or logged. A zero-rejection warning is fully accepted, not retried.
func metricsExportAcknowledged(body []byte) bool {
	fields, ok := metricsExportObject(body)
	if !ok {
		return false
	}
	partial, exists := fields["partialSuccess"]
	if !exists || bytes.Equal(bytes.TrimSpace(partial), []byte("null")) {
		return true
	}
	values, ok := metricsExportObject(partial)
	if !ok {
		return false
	}
	if message, exists := values["errorMessage"]; exists {
		var text string
		if !bytes.Equal(bytes.TrimSpace(message), []byte("null")) && json.Unmarshal(message, &text) != nil {
			return false
		}
	}
	rejected, exists := values["rejectedDataPoints"]
	if !exists || bytes.Equal(bytes.TrimSpace(rejected), []byte("null")) {
		return true
	}
	var decimal string
	if json.Unmarshal(rejected, &decimal) != nil {
		decimal = string(bytes.TrimSpace(rejected))
	}
	count, err := strconv.ParseInt(decimal, 10, 64)
	return err == nil && count == 0 || metricsExportZero.MatchString(decimal)
}

func metricsExportObject(body []byte) (map[string]json.RawMessage, bool) {
	d := json.NewDecoder(bytes.NewReader(body))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, false
	}
	values := map[string]json.RawMessage{}
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return nil, false
		}
		name, ok := key.(string)
		if !ok {
			return nil, false
		}
		if _, exists := values[name]; exists {
			return nil, false
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil, false
		}
		values[name] = value
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') {
		return nil, false
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, false
	}
	return values, true
}
