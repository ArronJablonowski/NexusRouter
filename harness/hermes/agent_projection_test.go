package hermes

import (
	"encoding/json"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"strings"
	"testing"
)

func TestAgentProjectionBindsNativeMessages(t *testing.T) {
	transcript := []providers.Message{
		{Role: "assistant", ToolCalls: []providers.ToolCall{{ID: "one", Name: "lookup", Arguments: json.RawMessage(`{"n":9007199254740993}`)}}},
		{Role: "tool", ToolCallID: "one", Content: "host result"},
		{Role: "assistant", Content: "answer"},
	}
	body := `{"version":1,"model":"fixture","text":"answer","messages":[{"role":"assistant","content":"","tool_calls":[{"id":"one","call_id":"one","type":"function","function":{"name":"nexus__lookup","arguments":"{\"n\":9007199254740993}"}}]},{"role":"tool","name":"nexus__lookup","tool_name":"nexus__lookup","tool_call_id":"one","content":"{\"result\":\"host result\"}"},{"role":"assistant","content":"answer"}]}`
	if p, e := parseAgentProjection([]byte(body), 0, "fixture", transcript); e != nil || p.Text != "answer" {
		t.Fatal(p, e)
	}
	for _, pair := range [][2]string{
		{`"version":1`, `"version":2`}, {`"model":"fixture"`, `"model":"other"`},
		{`"text":"answer"`, `"text":"invented"`}, {`"call_id":"one"`, `"call_id":"two"`},
		{`"tool_call_id":"one"`, `"tool_call_id":"two"`}, {"nexus__lookup", "lookup"},
		{"9007199254740993", "9007199254740992"}, {"host result", "invented"},
		{`\"result\"`, `\"error\"`}, {`"version":1`, `"version":1,"version":1`},
	} {
		if _, e := parseAgentProjection([]byte(strings.Replace(body, pair[0], pair[1], 1)), 0, "fixture", transcript); e == nil {
			t.Fatal("accepted changed evidence", pair)
		}
	}
	for _, sequence := range [][]providers.Message{nil, transcript[:2], {transcript[1], transcript[0], transcript[2]}} {
		if _, e := parseAgentProjection([]byte(body), 0, "fixture", sequence); e == nil {
			t.Fatal("accepted incomplete/reordered transcript")
		}
	}
	if _, e := parseAgentProjection([]byte(body), 1, "fixture", transcript); e == nil {
		t.Fatal("accepted failed process")
	}
	transcript[1].ToolFailed = true
	if _, e := parseAgentProjection([]byte(body), 0, "fixture", transcript); e == nil {
		t.Fatal("lost failure")
	}
	failed := strings.Replace(body, `\"result\"`, `\"error\"`, 1)
	if _, e := parseAgentProjection([]byte(failed), 0, "fixture", transcript); e != nil {
		t.Fatal("valid failed tool", e)
	}
}
