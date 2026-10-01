package goose

import (
	"encoding/json"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"os"
	"strings"
	"testing"
)

func TestCapturedAssistantFragments(t *testing.T) {
	body, err := os.ReadFile("testdata/native-fragments.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/native-fragments-transcript.json")
	if err != nil {
		t.Fatal(err)
	}
	var transcript []providers.Message
	if json.Unmarshal(raw, &transcript) != nil {
		t.Fatal("transcript")
	}
	model := "devstral-small-2:24b-instruct-2512-q4_K_M"
	got, err := parseAgentProjection(body, 0, model, transcript)
	if err != nil || got.Text != "answer" {
		t.Fatal(got, err)
	}
	for _, mutation := range []string{
		strings.Replace(string(body), `"text":"n"`, `"text":"nn"`, 1),
		strings.Replace(string(body), `"text":"n"`, `"text":""`, 1),
		strings.Replace(string(body), `"text":"n"`, `"text":"x"`, 1),
		strings.Replace(string(body), `"text":"n"}],"metadata":{"userVisible":true`, `"text":"n"}],"metadata":{"userVisible":false`, 1),
	} {
		if mutation == string(body) {
			t.Fatal("mutation did not apply")
		}
		if got, err := parseAgentProjection([]byte(mutation), 0, model, transcript); err == nil || got.Text != "" {
			t.Fatal("invalid fragments accepted", got)
		}
	}
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	final := strings.Join(lines[2:], "\n") + "\n"
	if got, err := ParseProjection([]byte(final), 0, "openai", model); err != nil || got.Text != "answer" {
		t.Fatal("text-only fragments", got, err)
	}
}

// A repeated delta must be retained, never silently deduplicated. Run binds the
// resulting text to the independently verified provider completion.
func TestRepeatedDeltaPreservesTextForCanonicalCheck(t *testing.T) {
	got, err := ParseProjection([]byte(message+message+complete), 0, "openai", "fixture")
	if err != nil || got.Text != "answeranswer" {
		t.Fatal(got, err)
	}
}
