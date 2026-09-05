package skills

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func validModelDraftJSON() []byte {
	return []byte(`{"version":1,"description":"Run project checks","tags":[],"steps":["Run tests"],"required_tools":[],"configuration":"","risks":[],"validation_cases":["Known passing fixture"]}`)
}

func TestParseModelDraftBindsAndCopiesHostProvenance(t *testing.T) {
	key := Key{Scope: "project", Name: "checks"}
	sessions, evidence := []string{"session-a", "session-b"}, []string{"check-a", "check-b"}
	d, err := parseModelDraft(validModelDraftJSON(), key, sessions, evidence)
	if err != nil || validateGeneratedDraft(d) != nil || d.Key != key || !reflect.DeepEqual(d.SourceSessions, sessions) || !reflect.DeepEqual(d.SourceEvidence, evidence) || d.Description != "Run project checks" || !reflect.DeepEqual(d.Steps, []string{"Run tests"}) {
		t.Fatal(d, err)
	}
	sessions[0] = "changed-session"
	evidence[0] = "changed-evidence"
	if d.SourceSessions[0] != "session-a" || d.SourceEvidence[0] != "check-a" {
		t.Fatal("host lists alias generated draft")
	}
	d.SourceSessions[1] = "draft-change"
	d.SourceEvidence[1] = "draft-change"
	if sessions[1] != "session-b" || evidence[1] != "check-b" {
		t.Fatal("draft aliases caller provenance")
	}
	full := bytes.Replace(validModelDraftJSON(), []byte(`"tags":[]`), []byte(`"tags":["code"]`), 1)
	full = bytes.Replace(full, []byte(`"required_tools":[]`), []byte(`"required_tools":["lookup"]`), 1)
	full = bytes.Replace(full, []byte(`"risks":[]`), []byte(`"risks":["Check tool permissions"]`), 1)
	if d, err := parseModelDraft(full, key, []string{"session"}, []string{"check"}); err != nil || len(d.Tags) != 1 || len(d.RequiredTools) != 1 || len(d.Risks) != 1 {
		t.Fatal("valid optional content rejected", d, err)
	}
}

func TestParseModelDraftRejectsMalformedOrUntrustedShape(t *testing.T) {
	good := validModelDraftJSON()
	replace := func(old, new string) []byte { return bytes.Replace(good, []byte(old), []byte(new), 1) }
	for name, raw := range map[string][]byte{
		"empty": nil, "null": []byte("null"), "array": []byte("[]"),
		"markdown":             append(append([]byte("```json\n"), good...), []byte("\n```")...),
		"trailing object":      append(append([]byte(nil), good...), []byte(" {}")...),
		"trailing garbage":     append(append([]byte(nil), good...), []byte(" private-invalid")...),
		"invalid UTF8":         replace("Run project checks", string([]byte{255})),
		"version":              replace(`"version":1`, `"version":2`),
		"string version":       replace(`"version":1`, `"version":"1"`),
		"unknown":              replace(`"version":1`, `"version":1,"unknown":true`),
		"duplicate":            replace(`"description":"Run project checks"`, `"description":"Run project checks","description":"override"`),
		"escaped duplicate":    replace(`"version":1`, `"version":1,"\u0076ersion":1`),
		"case alias":           replace(`"description":`, `"Description":`),
		"provenance sessions":  replace(`"version":1`, `"version":1,"source_sessions":["invented"]`),
		"provenance evidence":  replace(`"version":1`, `"version":1,"source_evidence":["invented"]`),
		"key":                  replace(`"version":1`, `"version":1,"key":{"scope":"other","name":"invented"}`),
		"object description":   replace(`"description":"Run project checks"`, `"description":{}`),
		"number configuration": replace(`"configuration":""`, `"configuration":1`),
		"object array item":    replace(`"steps":["Run tests"]`, `"steps":[{}]`),
		"nested array":         replace(`"steps":["Run tests"]`, `"steps":[["Run tests"]]`),
		"number array item":    replace(`"steps":["Run tests"]`, `"steps":[1]`),
		"empty steps":          replace(`"steps":["Run tests"]`, `"steps":[]`),
		"blank step":           replace(`"steps":["Run tests"]`, `"steps":[" \t"]`),
		"empty cases":          replace(`"validation_cases":["Known passing fixture"]`, `"validation_cases":[]`),
		"blank case":           replace(`"validation_cases":["Known passing fixture"]`, `"validation_cases":[" \n"]`),
		"blank description":    replace(`"description":"Run project checks"`, `"description":" "`),
		"invalid tool ID":      replace(`"required_tools":[]`, `"required_tools":["../tool"]`),
	} {
		t.Run(name, func(t *testing.T) {
			d, err := parseModelDraft(raw, Key{Scope: "project", Name: "checks"}, []string{"session"}, []string{"check"})
			if err != ErrValidation || d.Description != "" {
				t.Fatal("unsafe model draft admitted", d, err)
			}
		})
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(good, &fields); err != nil {
		t.Fatal(err)
	}
	for field := range fields {
		for _, mode := range []string{"missing", "null", "casealias", "duplicate"} {
			t.Run(field+"_"+mode, func(t *testing.T) {
				copy := map[string]json.RawMessage{}
				for k, v := range fields {
					copy[k] = v
				}
				switch mode {
				case "missing":
					delete(copy, field)
				case "null":
					copy[field] = json.RawMessage("null")
				case "casealias":
					copy[strings.ToUpper(field)] = copy[field]
					delete(copy, field)
				}
				raw, err := json.Marshal(copy)
				if err != nil {
					t.Fatal(err)
				}
				if mode == "duplicate" {
					prefix, _ := json.Marshal(field)
					raw = append(append(append(append([]byte(nil), raw[:len(raw)-1]...), ','), prefix...), ':')
					raw = append(raw, fields[field]...)
					raw = append(raw, '}')
				}
				if _, err := parseModelDraft(raw, Key{Scope: "project", Name: "checks"}, []string{"session"}, []string{"check"}); err != ErrValidation {
					t.Fatal("invalid required field admitted", field, mode, err)
				}
			})
		}
	}
	padded := append(append([]byte(nil), good...), []byte(strings.Repeat(" ", (64<<10)-len(good)))...)
	if _, err := parseModelDraft(padded, Key{Scope: "project", Name: "checks"}, []string{"session"}, []string{"check"}); err != nil {
		t.Fatal("exact model response bound rejected", err)
	}
	if _, err := parseModelDraft(append(padded, ' '), Key{Scope: "project", Name: "checks"}, []string{"session"}, []string{"check"}); err != ErrValidation {
		t.Fatal("oversized model response admitted", err)
	}
}
