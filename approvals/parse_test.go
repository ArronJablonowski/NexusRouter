package approvals

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestParseCommandStrictShape(t *testing.T) {
	c := Command{Expected: validRequest(), ID: "decision", Allowed: false}
	body, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseCommand(body)
	if err != nil || got.Allowed || got.ID != c.ID || !got.Expected.Matches(c.Expected) {
		t.Fatal("false command rejected", got, err)
	}
	for name, raw := range map[string][]byte{
		"empty": nil, "null": []byte("null"), "array": []byte("[]"), "trailing": append(append([]byte(nil), body...), []byte(" {}")...), "invalid UTF8": append(append([]byte(nil), body...), 255),
		"unknown":           bytes.Replace(body, []byte(`"allowed":false`), []byte(`"allowed":false,"actor":"operator"`), 1),
		"duplicate":         bytes.Replace(body, []byte(`"allowed":false`), []byte(`"allowed":false,"allowed":true`), 1),
		"case alias":        bytes.Replace(body, []byte(`"allowed":false`), []byte(`"Allowed":false`), 1),
		"null action":       bytes.Replace(body, []byte(`"allowed":false`), []byte(`"allowed":null`), 1),
		"string action":     bytes.Replace(body, []byte(`"allowed":false`), []byte(`"allowed":"false"`), 1),
		"missing action":    bytes.Replace(body, []byte(`,"allowed":false`), nil, 1),
		"nested unknown":    bytes.Replace(body, []byte(`"version":1`), []byte(`"version":1,"actor":"operator"`), 1),
		"nested duplicate":  bytes.Replace(body, []byte(`"version":1`), []byte(`"version":1,"version":1`), 1),
		"nested alias":      bytes.Replace(body, []byte(`"version":1`), []byte(`"Version":1`), 1),
		"nested null":       bytes.Replace(body, []byte(`"version":1`), []byte(`"version":null`), 1),
		"nested missing":    bytes.Replace(body, []byte(`"version":1,`), nil, 1),
		"escaped duplicate": bytes.Replace(body, []byte(`"allowed":false`), []byte(`"allowed":false,"\u0061llowed":true`), 1),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := ParseCommand(raw)
			if err != ErrInvalid || got.ID != "" {
				t.Fatal("malformed command admitted", got, err)
			}
		})
	}
	// Every required top-level and nested key independently rejects absence/null.
	var fields map[string]json.RawMessage
	json.Unmarshal(body, &fields)
	for key := range fields {
		for _, null := range []bool{false, true} {
			copy := map[string]json.RawMessage{}
			for k, v := range fields {
				copy[k] = v
			}
			if null {
				copy[key] = json.RawMessage("null")
			} else {
				delete(copy, key)
			}
			raw, _ := json.Marshal(copy)
			if _, err := ParseCommand(raw); err != ErrInvalid {
				t.Fatal("missing/null top key admitted", key, null)
			}
		}
	}
	var expected map[string]json.RawMessage
	json.Unmarshal(fields["expected"], &expected)
	for key := range expected {
		for _, null := range []bool{false, true} {
			copy := map[string]json.RawMessage{}
			for k, v := range expected {
				copy[k] = v
			}
			if null {
				copy[key] = json.RawMessage("null")
			} else {
				delete(copy, key)
			}
			nested, _ := json.Marshal(copy)
			top := map[string]json.RawMessage{"expected": nested, "id": fields["id"], "allowed": fields["allowed"]}
			raw, _ := json.Marshal(top)
			if _, err := ParseCommand(raw); err != ErrInvalid {
				t.Fatal("missing/null expected key admitted", key, null)
			}
		}
	}
	padded := append(append([]byte(nil), body...), []byte(strings.Repeat(" ", (16<<10)-len(body)))...)
	if _, err := ParseCommand(padded); err != nil {
		t.Fatal("exact size rejected", err)
	}
	if _, err := ParseCommand(append(padded, ' ')); err != ErrInvalid {
		t.Fatal("oversized command admitted", err)
	}
}
