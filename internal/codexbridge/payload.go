package codexbridge

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"unicode/utf8"
)

// Reject duplicate keys and invalid encodings before typed decoding so wire
// controls cannot acquire ambiguous last-key-wins meanings. Unknown fields are
// allowed for forward-compatible metadata, not dispatched as capabilities.
func decodePayload(raw json.RawMessage, out any) error {
	if len(raw) > 1<<20 || !utf8.Valid(raw) || !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
		return failure(false)
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var walk func(int) bool
	walk = func(depth int) bool {
		if depth > 64 {
			return false
		}
		t, err := d.Token()
		if err != nil {
			return false
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return true
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				name, ok := key.(string)
				if err != nil || !ok || seen[name] {
					return false
				}
				seen[name] = true
				if !walk(depth + 1) {
					return false
				}
			}
		case '[':
			for d.More() {
				if !walk(depth + 1) {
					return false
				}
			}
		default:
			return false
		}
		_, err = d.Token()
		return err == nil
	}
	if !walk(0) {
		return failure(false)
	}
	if _, err := d.Token(); err != io.EOF {
		return failure(false)
	}
	if !canonicalControls(raw, reflect.TypeOf(out)) || json.Unmarshal(raw, out) != nil {
		return failure(false)
	}
	return nil
}

// encoding/json accepts case-insensitive struct keys. Protocol controls must
// use their canonical spelling; raw arguments/schemas remain opaque and may
// legitimately contain case-distinct application keys.
func canonicalControls(raw json.RawMessage, typ reflect.Type) bool {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return true
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return false
	}
	for key, value := range fields {
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "" {
				name = field.Name
			}
			if strings.EqualFold(key, name) {
				if key != name || !canonicalControls(value, field.Type) {
					return false
				}
				break
			}
		}
	}
	return true
}
