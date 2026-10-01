package goose

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/providers"
)

func agentProjectionFixture() (string, []providers.Message) {
	transcript := []providers.Message{
		{Role: "assistant", Content: "checking", ToolCalls: []providers.ToolCall{{ID: "call-1", Name: "lookup", Arguments: json.RawMessage(`{"n":9007199254740993}`)}}},
		{Role: "tool", ToolCallID: "call-1", Content: "host result"},
		{Role: "assistant", Content: "answer"},
	}
	body := `{"type":"message","message":{"id":"a","role":"assistant","content":[{"type":"text","text":"checking"},{"type":"toolRequest","id":"call-1","toolCall":{"status":"success","value":{"name":"nexus__lookup","arguments":{"n":9007199254740993}}}}],"metadata":{"inference":{"provider":"openai","requestedModel":"fixture"}}}}` + "\n" +
		`{"type":"message","message":{"id":"b","role":"user","content":[{"type":"toolResponse","id":"call-1","toolResult":{"status":"success","value":{"content":[{"type":"text","text":"host result"}],"isError":false}}}]}}` + "\n" +
		`{"type":"message","message":{"id":"c","role":"assistant","content":[{"type":"text","text":"answer"}],"metadata":{"inference":{"provider":"openai","requestedModel":"fixture"}}}}` + "\n" +
		`{"type":"complete","total_tokens":0}` + "\n"
	return body, transcript
}
func TestAgentProjectionRequiresCompleteHostTranscript(t *testing.T) {
	body, transcript := agentProjectionFixture()
	p, e := parseAgentProjection([]byte(body), 0, "fixture", transcript)
	if e != nil || p.Text != "answer" {
		t.Fatal(p, e)
	}
	for _, pair := range [][2]string{
		{"nexus__lookup", "lookup"}, {"9007199254740993", "9007199254740992"}, {"host result", "fabricated"}, {`"isError":false`, `"isError":true`},
		{`"status":"success"`, `"status":"error"`}, {`"requestedModel":"fixture"`, `"requestedModel":"other"`}, {`"role":"user"`, `"role":"system"`},
		{`"id":"b"`, `"id":"a"`}, {`"type":"toolResponse"`, `"type":"image"`}, {`"total_tokens":0`, `"total_tokens":-1`},
		{`"text":"answer"`, `"text":"fake answer"`}, {`"name":"nexus__lookup"`, `"name":"nexus__lookup","name":"nexus__shell"`},
	} {
		changed := strings.Replace(body, pair[0], pair[1], 1)
		if _, e := parseAgentProjection([]byte(changed), 0, "fixture", transcript); e == nil {
			t.Fatal("accepted mismatch", pair)
		}
	}
	lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	for _, changed := range []string{
		strings.Join([]string{lines[0], lines[2], lines[3]}, "\n") + "\n",
		strings.Join([]string{lines[0], lines[1], lines[1], lines[2], lines[3]}, "\n") + "\n",
		strings.Join([]string{lines[1], lines[0], lines[2], lines[3]}, "\n") + "\n",
		strings.Join(lines[:3], "\n") + "\n", body + lines[3] + "\n", strings.TrimSuffix(body, "\n"),
	} {
		if _, e := parseAgentProjection([]byte(changed), 0, "fixture", transcript); e == nil {
			t.Fatal("accepted incomplete/replayed lifecycle")
		}
	}
	if _, e := parseAgentProjection([]byte(body), 1, "fixture", transcript); e == nil {
		t.Fatal("accepted failed native process")
	}
}
