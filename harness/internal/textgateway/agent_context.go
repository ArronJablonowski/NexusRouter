package textgateway

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/ArronJablonowski/NexusRouter/providers"
)

func copyAgentMessages(messages []providers.Message) ([]providers.Message, error) {
	if providers.ValidateMessages(messages) != nil {
		return nil, ErrProjection
	}
	body, e := json.Marshal(messages)
	if e != nil || len(body) > MaxRecordBytes || !uniqueJSON(body) {
		return nil, ErrProjection
	}
	var copy []providers.Message
	if json.Unmarshal(body, &copy) != nil {
		return nil, ErrProjection
	}
	for _, m := range copy {
		if !utf8.ValidString(m.Content) {
			return nil, ErrProjection
		}
	}
	return copy, nil
}
func agentSchemas(tools []providers.Tool) (json.RawMessage, error) {
	schemas := make([]any, 0, len(tools))
	names := map[string]bool{}
	for _, t := range tools {
		if !agentToolName(t.Name) || names[t.Name] || len(t.Description) > 65536 || !utf8.ValidString(t.Description) || !uniqueJSON(t.Parameters) {
			return nil, ErrProjection
		}
		var parameters map[string]json.RawMessage
		if json.Unmarshal(t.Parameters, &parameters) != nil || parameters == nil {
			return nil, ErrProjection
		}
		var kind string
		if json.Unmarshal(parameters["type"], &kind) != nil || kind != "object" {
			return nil, ErrProjection
		}
		names[t.Name] = true
		schemas = append(schemas, map[string]any{"type": "function", "function": map[string]any{"name": t.Name, "description": t.Description, "parameters": json.RawMessage(t.Parameters)}})
	}
	body, e := json.Marshal(schemas)
	if e != nil || len(body) > MaxRecordBytes/2 {
		return nil, ErrProjection
	}
	return body, nil
}
func agentToolName(name string) bool {
	if len(name) < 1 || len(name) > 64 {
		return false
	}
	return !strings.ContainsFunc(name, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-')
	})
}
func (g *AgentGateway) requestBody() ([]byte, error) {
	if providers.ValidateMessages(g.messages) != nil {
		return nil, ErrProjection
	}
	messages := make([]any, 0, len(g.messages))
	native := g.config.UpstreamProtocol == "ollama"
	callNames := map[string]string{}
	for _, m := range g.messages {
		entry := map[string]any{"role": m.Role, "content": providers.ToolResultContent(m)}
		if m.Role == "tool" {
			if native {
				name, ok := callNames[m.ToolCallID]
				if !ok {
					return nil, ErrProjection
				}
				entry["tool_name"] = name
			} else {
				entry["tool_call_id"] = m.ToolCallID
			}
		}
		if len(m.ToolCalls) > 0 {
			calls := make([]any, 0, len(m.ToolCalls))
			for _, c := range m.ToolCalls {
				if !identifier(c.ID) || !agentToolName(c.Name) || !uniqueJSON(c.Arguments) {
					return nil, ErrProjection
				}
				callNames[c.ID] = c.Name
				var args any = string(c.Arguments)
				if native {
					args = json.RawMessage(c.Arguments)
				}
				calls = append(calls, map[string]any{"id": c.ID, "type": "function", "function": map[string]any{"name": c.Name, "arguments": args}})
			}
			entry["tool_calls"] = calls
		}
		messages = append(messages, entry)
	}
	fields := map[string]any{"model": g.config.Model, "messages": messages, "stream": true, "stream_options": map[string]bool{"include_usage": true}, "max_tokens": g.config.MaxOutputTokens, "store": false}
	if !g.ended {
		fields["tools"] = g.tools
		fields["tool_choice"] = "auto"
		fields["parallel_tool_calls"] = false
	}
	if native {
		delete(fields, "stream_options")
		delete(fields, "max_tokens")
		delete(fields, "store")
		delete(fields, "tool_choice")
		delete(fields, "parallel_tool_calls")
		fields["think"] = false
		fields["options"] = map[string]int{"num_ctx": g.config.ContextTokens, "num_predict": g.config.MaxOutputTokens}
	}
	body, e := json.Marshal(fields)
	// Conservative byte-based context bound includes schemas and tool results.
	// No implicit compaction or silent context replacement is permitted.
	if e != nil || len(body) > MaxRecordBytes || len(body)+4096+g.config.MaxOutputTokens > g.config.ContextTokens {
		return nil, ErrProjection
	}
	return body, nil
}
func validAgentRequest(body []byte, model string, limit int, defaultLimit bool) bool {
	if !uniqueJSON(body) {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil || fields == nil {
		return false
	}
	for k := range fields {
		switch k {
		case "model", "messages", "stream", "stream_options", "max_tokens", "max_completion_tokens", "temperature", "top_p", "frequency_penalty", "presence_penalty", "stop", "reasoning_effort", "seed", "store", "tools", "tool_choice", "parallel_tool_calls":
		default:
			return false
		}
	}
	var messages []json.RawMessage
	if json.Unmarshal(fields["messages"], &messages) != nil || len(messages) == 0 || len(messages) > 512 {
		return false
	}
	if raw, ok := fields["tools"]; ok {
		var tools []json.RawMessage
		if json.Unmarshal(raw, &tools) != nil || len(tools) > 128 {
			return false
		}
	}
	if raw, ok := fields["tool_choice"]; ok {
		var mode string
		if json.Unmarshal(raw, &mode) != nil || mode != "auto" {
			return false
		}
	}
	if raw, ok := fields["parallel_tool_calls"]; ok {
		var enabled bool
		if string(raw) == "null" || json.Unmarshal(raw, &enabled) != nil {
			return false
		}
	}
	delete(fields, "tools")
	delete(fields, "tool_choice")
	delete(fields, "parallel_tool_calls")
	// Child history is deliberately not used. Validate the remaining envelope
	// against the same pinned model and generation ceilings as text-only runs.
	fields["messages"] = json.RawMessage(`[{"role":"user","content":"host context"}]`)
	stripped, e := json.Marshal(fields)
	if e != nil {
		return false
	}
	if defaultLimit {
		stripped, e = supplyOutputLimit(stripped, limit)
		if e != nil {
			return false
		}
	}
	return validGatewayRequest(stripped, model, limit)
}
