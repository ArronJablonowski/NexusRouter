package goose

import (
	"encoding/json"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"os"
	"testing"
)

func TestAgentIdentityAndOwnedConfiguration(t *testing.T) {
	executable, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	c := AgentConfig{Config: runnerFixture(t, executable), Tools: agentSchema(), MaxTurns: 3}
	c.Messages = []providers.Message{{Role: "user", Content: "original", ToolCalls: []providers.ToolCall{{ID: "one", Name: "nexus_lookup", Arguments: json.RawMessage(`{"path":"original"}`)}}}}
	snapshot, id, e := prepareAgent(c, "prompt")
	if e != nil {
		t.Fatal(e)
	}
	legacy, e := c.Config.Identity()
	if e != nil || legacy == id || id.AdapterVersion != AgentAdapterVersion {
		t.Fatal("tool evidence shares legacy identity", e)
	}
	c.Tools[0].Description = "changed"
	c.Tools[0].Parameters[0] = '['
	c.Messages[0].Content = "changed"
	c.Messages[0].ToolCalls[0].Arguments[0] = '['
	c.Prices.Input = 9
	again, e := snapshot.Identity()
	if e != nil || again != id || snapshot.Messages[0].Content != "original" || string(snapshot.Messages[0].ToolCalls[0].Arguments) != `{"path":"original"}` {
		t.Fatal("caller mutation altered snapshot", e)
	}
	changed := snapshot
	changed.MaxTurns++
	next, e := changed.Identity()
	if e != nil || next == id {
		t.Fatal("turn policy omitted", e)
	}
	for _, schema := range []string{`{"type":"array"}`, `{"type":"object","type":"object"}`, `null`} {
		changed.Tools = agentSchema()
		changed.Tools[0].Parameters = json.RawMessage(schema)
		if _, e := changed.Identity(); e == nil {
			t.Fatal("invalid schema admitted", schema)
		}
	}
	if _, e := RunAgent(nil, snapshot, "prompt", nil); e == nil {
		t.Fatal("unowned session accepted")
	}
}
