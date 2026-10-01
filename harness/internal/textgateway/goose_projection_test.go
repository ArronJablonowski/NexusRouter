package textgateway

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestGooseProjectionPreservesCanonicalEvidence(t *testing.T) {
	for _, stream := range []string{
		oneAgentTool(),
		agentChunk(agentCall(0, "id", "read", `{"n":9007199254740993,"name":"read"}`), nil) + agentChunk(`{"tool_calls":[{"index":0,"function":{"name":"read","arguments":" "}}]}`, "tool_calls") + "data: [DONE]\n\n",
		completionFixture("model"),
	} {
		original, e := VerifyAgentCompletion(strings.NewReader(stream), "model")
		if e != nil {
			t.Fatal(e)
		}
		before, _ := json.Marshal(original)
		output, e := gooseToolProjection(original.Stream, "model")
		if e != nil {
			t.Fatal(e)
		}
		projected, e := VerifyAgentCompletion(bytes.NewReader(output), "model")
		if e != nil {
			t.Fatal(e)
		}
		after, _ := json.Marshal(original)
		if !bytes.Equal(before, after) {
			t.Fatal("mutated canonical input")
		}
		if projected.Text != original.Text || !reflect.DeepEqual(projected.Usage, original.Usage) {
			t.Fatal("changed text or usage")
		}
		for i, c := range original.Calls {
			got := projected.Calls[i]
			if got.ID != c.ID || got.Name != "nexus__"+c.Name || !bytes.Equal(got.Arguments, c.Arguments) {
				t.Fatal("changed arguments/id", got, c)
			}
		}
	}
	for _, stream := range []string{
		strings.Replace(oneAgentTool(), "data: [DONE]\n\n", "", 1),
		strings.Replace(oneAgentTool(), `"model":"model"`, `"model":"other"`, 1),
		strings.Replace(oneAgentTool(), `"name":"read"`, `"name":"read","name":"shell"`, 1),
	} {
		if _, e := gooseToolProjection([]byte(stream), "model"); e == nil {
			t.Fatal("unverified stream projected")
		}
	}
}
