package openhands

import (
	"os"
	"strings"
	"testing"
)

func TestCapturedSDKProjection(t *testing.T) {
	b, err := os.ReadFile("testdata/native-1.50.1.json")
	if err != nil {
		t.Fatal(err)
	}
	p, err := ParseProjection(b, 0, "openai/fixture-model")
	if err != nil || p.Text != "native OpenHands fixture" || p.ResponseID != "chatcmpl-fixture" {
		t.Fatal(p, err)
	}
	for _, tc := range []struct{ name, old, new string }{
		{"status", "finished", "paused"},
		{"version", "1.50.1", "1.21.0"},
		{"model", "openai/fixture-model", "other"},
		{"tool", "\"tool_calls\": null", "\"tool_calls\": [{}]"},
		{"reasoning", "\"reasoning_content\": null", "\"reasoning_content\": \"hidden\""},
		{"event", "MessageEvent", "ActionEvent"},
		{"media", "\"type\": \"text\"", "\"type\": \"image\""},
		{"role", "assistant", "user"},
		{"duplicate", "\"status\": \"finished\"", "\"status\": \"finished\", \"STATUS\": \"finished\""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mutated := strings.Replace(string(b), tc.old, tc.new, 1)
			if mutated == string(b) {
				t.Fatal("mutation missed")
			}
			p, err := ParseProjection([]byte(mutated), 0, "openai/fixture-model")
			if err == nil || p != (Projection{}) {
				t.Fatal("invalid projection accepted", p, err)
			}
		})
	}
	for _, bad := range [][]byte{b[:len(b)/2], append(append([]byte{}, b...), b...), []byte("{}"), append([]byte("banner\n"), b...)} {
		if p, err := ParseProjection(bad, 0, "openai/fixture-model"); err == nil || p != (Projection{}) {
			t.Fatal("bad framing accepted")
		}
	}
	if _, err := ParseProjection(b, 1, "openai/fixture-model"); err == nil {
		t.Fatal("failed process accepted")
	}
}
