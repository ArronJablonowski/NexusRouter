package openclaw

import (
	"bytes"
	"encoding/json"
	"github.com/ArronJablonowski/NexusRouter/harness/internal/wirejson"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"io"
	"os"
	"reflect"
	"strings"
)

func parseAgentProjection(body []byte, exit int, provider, model, receiptPath string, transcript []providers.Message) (Projection, error) {
	bad := func() (Projection, error) { return Projection{}, ErrProjection }
	if exit != 0 || len(transcript) == 0 || providers.ValidateMessages(transcript) != nil || len(body) > MaxEnvelopeBytes || !wirejson.Unique(body) {
		return bad()
	}
	f, e := os.Open(receiptPath)
	if e != nil {
		return bad()
	}
	defer f.Close()
	raw, e := io.ReadAll(io.LimitReader(f, MaxEnvelopeBytes+1))
	if e != nil || len(raw) > MaxEnvelopeBytes {
		return bad()
	}
	var lines [][]byte
	if len(raw) > 0 {
		if raw[len(raw)-1] != '\n' {
			return bad()
		}
		lines = bytes.Split(raw[:len(raw)-1], []byte{'\n'})
	}
	calls := map[string]providers.ToolCall{}
	names := map[string]bool{}
	turns, failures, index := 0, 0, 0
	for _, m := range transcript {
		if m.Role == "assistant" {
			turns++
			for _, c := range m.ToolCalls {
				calls[c.ID] = c
				names["nexus__"+c.Name] = true
			}
			continue
		}
		if m.Role != "tool" || index >= len(lines) || !wirejson.Unique(lines[index]) {
			return bad()
		}
		var receipt struct {
			ID, Name string
			Args     json.RawMessage
			Content  string
			Failed   *bool
			End      *bool `json:"end_tool_use"`
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(lines[index], &receipt) != nil || json.Unmarshal(lines[index], &fields) != nil || len(fields) != 6 || receipt.Failed == nil || receipt.End == nil {
			return bad()
		}
		c, ok := calls[m.ToolCallID]
		if !ok || receipt.ID != c.ID || receipt.Name != c.Name || receipt.Content != m.Content || *receipt.Failed != m.ToolFailed || !agentArgumentsEqual(receipt.Args, c.Arguments) {
			return bad()
		}
		if m.ToolFailed {
			failures++
		}
		index++
	}
	if index != len(lines) {
		return bad()
	}
	var native struct {
		AssistantTurns int
		ToolSummary    *struct {
			Calls, Failures int
			Tools           []string
		}
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &native) != nil || json.Unmarshal(body, &fields) != nil || native.AssistantTurns != turns {
		return bad()
	}
	if native.ToolSummary == nil {
		if index != 0 {
			return bad()
		}
	} else if native.ToolSummary.Calls != index || native.ToolSummary.Failures != failures || len(native.ToolSummary.Tools) != len(names) {
		return bad()
	}
	seen := map[string]bool{}
	var summaryTools []string
	if native.ToolSummary != nil {
		summaryTools = native.ToolSummary.Tools
	}
	for _, name := range summaryTools {
		if !names[name] || seen[name] {
			return bad()
		}
		seen[name] = true
	}
	// The legacy validator still checks model, exit, payload, usage shape and flags.
	// Replace only the counters already independently reconciled above.
	fields["assistantTurns"] = json.RawMessage(`1`)
	delete(fields, "toolSummary")
	normalized, e := json.Marshal(fields)
	if e != nil {
		return bad()
	}
	p, e := ParseProjection(normalized, exit, provider, model)
	if e != nil {
		return bad()
	}
	last := transcript[len(transcript)-1]
	if last.Role != "assistant" || len(last.ToolCalls) != 0 || p.Text != strings.TrimRightFunc(last.Content, jsWhitespace) {
		return bad()
	}
	return p, nil
}
func agentArgumentsEqual(a, b json.RawMessage) bool {
	decode := func(raw json.RawMessage) (map[string]any, bool) {
		if len(raw) > 64<<10 || !wirejson.Unique(raw) {
			return nil, false
		}
		var v map[string]any
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		if d.Decode(&v) != nil || v == nil {
			return nil, false
		}
		return v, true
	}
	x, ok := decode(a)
	if !ok {
		return false
	}
	y, ok := decode(b)
	return ok && reflect.DeepEqual(x, y)
}
