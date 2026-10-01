package goose

import (
	"os"
	"strings"
	"testing"
)

const message = `{"type":"message","message":{"id":"fixture","role":"assistant","content":[{"type":"text","text":"answer"}],"metadata":{"inference":{"provider":"openai","requestedModel":"fixture"}}}}` + "\n"
const complete = `{"type":"complete","total_tokens":0,"input_tokens":0,"output_tokens":0}` + "\n"

func TestProjection(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"success", message + complete, true},
		{"unknown-usage", message + `{"type":"complete","total_tokens":null}` + "\n", true},
		{"banner", "Goose ready\n" + message + complete, false},
		{"missing-complete", message, false},
		{"distinct-message", message + strings.Replace(message, `"id":"fixture"`, `"id":"other"`, 1) + complete, false},
		{"duplicate-complete", message + complete + complete, false},
		{"wrong-model", strings.Replace(message, "requestedModel\":\"fixture", "requestedModel\":\"other", 1) + complete, false},
		{"wrong-provider", strings.Replace(message, "openai", "other", 1) + complete, false},
		{"tool", strings.Replace(message, `"type":"text"`, `"type":"toolRequest"`, 1) + complete, false},
		{"error", message + `{"type":"error","error":"failed"}` + "\n" + complete, false},
		{"negative-count", message + strings.Replace(complete, `"total_tokens":0`, `"total_tokens":-1`, 1), false},
		{"duplicate-key", message + strings.Replace(complete, `"total_tokens":0`, `"total_tokens":1,"TOTAL_TOKENS":0`, 1), false},
		{"missing-LF", message + strings.TrimSuffix(complete, "\n"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := ParseProjection([]byte(tc.body), 0, "openai", "fixture")
			if (err == nil) != tc.valid {
				t.Fatal(err)
			}
			if !tc.valid && p != (Projection{}) {
				t.Fatal("partial result")
			}
			if tc.valid && p.Text != "answer" {
				t.Fatal("lost answer")
			}
		})
	}
	if _, err := ParseProjection([]byte(message+complete), 1, "openai", "fixture"); err == nil {
		t.Fatal("failed process accepted")
	}
}

func TestCapturedNativeProjection(t *testing.T) {
	b, err := os.ReadFile("testdata/native-1.52.0.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	p, err := ParseProjection(b, 0, "openai", "fixture")
	if err != nil || p.Text != "answer" {
		t.Fatal("captured native protocol mismatch", err)
	}
}
