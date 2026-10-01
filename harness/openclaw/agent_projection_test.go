package openclaw

import (
	"encoding/json"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentProjectionBindsReceipts(t *testing.T) {
	transcript := []providers.Message{{Role: "assistant", ToolCalls: []providers.ToolCall{{ID: "original-0", Name: "lookup", Arguments: json.RawMessage(`{"n":9007199254740993}`)}}}, {Role: "tool", ToolCallID: "original-0", Content: "host result"}, {Role: "assistant", Content: "answer"}}
	body := `{"ok":true,"status":"ok","final":"answer","provider":"fixture","model":"model","sessionId":"session","assistantTurns":2,"payloads":[{"text":"answer"}],"toolSummary":{"calls":1,"failures":0,"tools":["nexus__lookup"]}}`
	receipt := `{"id":"original-0","name":"lookup","args":{"n":9007199254740993},"content":"host result","failed":false,"end_tool_use":false}` + "\n"
	path := filepath.Join(t.TempDir(), "receipts")
	write := func(s string) {
		t.Helper()
		if e := os.WriteFile(path, []byte(s), 0600); e != nil {
			t.Fatal(e)
		}
	}
	write(receipt)
	if p, e := parseAgentProjection([]byte(body), 0, "fixture", "model", path, transcript); e != nil || p.Text != "answer" {
		t.Fatal(p, e)
	}
	for _, pair := range [][2]string{{"original-0", "original0"}, {"9007199254740993", "9007199254740992"}, {"host result", "invented"}, {`"failed":false`, `"failed":true`}, {`"id":"original-0"`, `"id":"original-0","id":"other"`}} {
		write(strings.Replace(receipt, pair[0], pair[1], 1))
		if _, e := parseAgentProjection([]byte(body), 0, "fixture", "model", path, transcript); e == nil {
			t.Fatal("accepted changed receipt", pair)
		}
	}
	for _, r := range []string{"", receipt + receipt, strings.TrimSpace(receipt)} {
		write(r)
		if _, e := parseAgentProjection([]byte(body), 0, "fixture", "model", path, transcript); e == nil {
			t.Fatal("accepted incomplete or duplicate receipts")
		}
	}
	write(receipt)
	for _, pair := range [][2]string{{`"assistantTurns":2`, `"assistantTurns":1`}, {`"calls":1`, `"calls":0`}, {`"failures":0`, `"failures":1`}, {"nexus__lookup", "unknown"}, {`"final":"answer"`, `"final":"invented"`}, {`"ok":true`, `"ok":false`}} {
		if _, e := parseAgentProjection([]byte(strings.Replace(body, pair[0], pair[1], 1)), 0, "fixture", "model", path, transcript); e == nil {
			t.Fatal("accepted changed summary", pair)
		}
	}
	if _, e := parseAgentProjection([]byte(body), 1, "fixture", "model", path, transcript); e == nil {
		t.Fatal("accepted failed process")
	}
	if _, e := parseAgentProjection([]byte(body), 0, "fixture", "model", path, nil); e == nil {
		t.Fatal("accepted no canonical transcript")
	}
	transcript[1].ToolFailed = true
	write(strings.Replace(receipt, `"failed":false`, `"failed":true`, 1))
	if _, e := parseAgentProjection([]byte(strings.Replace(body, `"failures":0`, `"failures":1`, 1)), 0, "fixture", "model", path, transcript); e != nil {
		t.Fatal("valid recoverable failure", e)
	}
	write("")
	direct := `{"ok":true,"status":"ok","final":"answer","provider":"fixture","model":"model","sessionId":"session","assistantTurns":1,"payloads":[{"text":"answer"}]}`
	if _, e := parseAgentProjection([]byte(direct), 0, "fixture", "model", path, transcript[2:]); e != nil {
		t.Fatal("valid direct answer", e)
	}
}
