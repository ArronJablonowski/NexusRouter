package approvals

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func TestApprovalBehaviorExactAndStrictCommand(t *testing.T) {
	for _, behavior := range []runtime.ToolBehavior{"", runtime.BehaviorReadOnly, runtime.BehaviorIdempotentWrite, runtime.BehaviorNonIdempotentWrite} {
		req := validRequest()
		req.ToolBehavior = behavior
		if req.Validate() != nil {
			t.Fatal(behavior)
		}
		command := Command{Expected: req, ID: "decision", Allowed: true}
		body, _ := json.Marshal(command)
		got, err := ParseCommand(body)
		if err != nil || !got.Expected.Matches(req) {
			t.Fatal(got, err)
		}
		if behavior == "" {
			if bytes.Contains(body, []byte("tool_behavior")) {
				t.Fatal("legacy changed")
			}
			continue
		}
		other := req
		other.ToolBehavior = ""
		if req.Matches(other) {
			t.Fatal("missing class matches typed approval")
		}
		fragment, _ := json.Marshal(behavior)
		needle := append([]byte(`"tool_behavior":`), fragment...)
		for _, replacement := range []string{`"tool_behavior":null`, `"tool_behavior":""`, `"tool_behavior":"unsafe"`, `"Tool_Behavior":"read_only"`, `"tool_behavior":"read_only","tool_behavior":"idempotent_write"`} {
			if _, err := ParseCommand(bytes.Replace(body, needle, []byte(replacement), 1)); err == nil {
				t.Fatal(replacement)
			}
		}
	}
}
