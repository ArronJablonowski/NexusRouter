package hermes

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/ArronJablonowski/NexusRouter/harness/internal/wirejson"
	"github.com/ArronJablonowski/NexusRouter/providers"
)

// The pinned embedding envelope carries native messages, not authority. Every
// generated message must match the completed host transcript before acceptance.
func parseAgentProjection(body []byte, exit int, model string, transcript []providers.Message) (Projection, error) {
	bad := func() (Projection, error) { return Projection{}, ErrProjection }
	if len(transcript) == 0 || exit != 0 || len(body) == 0 || len(body) > MaxStreamBytes || !utf8.Valid(body) || !wirejson.Unique(body) || providers.ValidateMessages(transcript) != nil {
		return bad()
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil || len(fields) != 4 {
		return bad()
	}
	for k := range fields {
		if k != "version" && k != "model" && k != "text" && k != "messages" {
			return bad()
		}
	}
	var envelope struct {
		Version     int
		Model, Text string
		Messages    []struct {
			Role, Content, Name string
			ToolName            string `json:"tool_name"`
			ToolCallID          string `json:"tool_call_id"`
			ToolCalls           []struct {
				ID, Type string
				CallID   string `json:"call_id"`
				Function struct{ Name, Arguments string }
			} `json:"tool_calls"`
		}
	}
	if json.Unmarshal(body, &envelope) != nil || envelope.Version != 1 || envelope.Model != model || strings.TrimSpace(envelope.Text) == "" || len(envelope.Text) > MaxTextBytes || len(envelope.Messages) != len(transcript) {
		return bad()
	}
	names := map[string]string{}
	for i, m := range envelope.Messages {
		want := transcript[i]
		if m.Role != want.Role {
			return bad()
		}
		switch m.Role {
		case "assistant":
			if m.Content != want.Content || m.ToolCallID != "" || len(m.ToolCalls) != len(want.ToolCalls) {
				return bad()
			}
			for j, c := range m.ToolCalls {
				call := want.ToolCalls[j]
				if c.ID != call.ID || (c.CallID != "" && c.CallID != call.ID) || c.Type != "function" || c.Function.Name != "nexus__"+call.Name || !agentArgumentsEqual([]byte(c.Function.Arguments), call.Arguments) {
					return bad()
				}
				names[call.ID] = c.Function.Name
			}
		case "tool":
			name := names[want.ToolCallID]
			if name == "" || m.ToolCallID != want.ToolCallID || m.Name != name || m.ToolName != name || len(m.ToolCalls) != 0 || !wirejson.Unique([]byte(m.Content)) {
				return bad()
			}
			var result map[string]json.RawMessage
			if json.Unmarshal([]byte(m.Content), &result) != nil || len(result) != 1 {
				return bad()
			}
			key := "result"
			if want.ToolFailed {
				key = "error"
			}
			var content string
			if json.Unmarshal(result[key], &content) != nil || content != want.Content {
				return bad()
			}
		default:
			return bad()
		}
	}
	last := transcript[len(transcript)-1]
	if last.Role != "assistant" || len(last.ToolCalls) != 0 || last.Content != envelope.Text {
		return bad()
	}
	return Projection{ConfiguredModel: model, Text: envelope.Text}, nil
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
