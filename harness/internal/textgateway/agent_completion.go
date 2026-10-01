package textgateway

import (
	"bytes"
	"encoding/json"

	"github.com/ArronJablonowski/NexusRouter/providers"
)

const maxAgentCalls = 128
const maxAgentArguments = 64 << 10

type streamedTool struct {
	id, name  string
	arguments []byte
}
type streamedTools []streamedTool

// New indices are contiguous; continuation fragments may only add arguments.
// Stable repeated metadata is tolerated, but changes never retarget a call.
func (s *streamedTools) append(raw json.RawMessage) bool {
	var deltas []json.RawMessage
	if json.Unmarshal(raw, &deltas) != nil || len(deltas) == 0 || len(deltas) > maxAgentCalls {
		return false
	}
	seen := map[int]bool{}
	for _, delta := range deltas {
		var fields map[string]json.RawMessage
		if json.Unmarshal(delta, &fields) != nil || fields == nil {
			return false
		}
		for k := range fields {
			if k != "index" && k != "id" && k != "type" && k != "function" {
				return false
			}
		}
		var index int
		if len(fields["index"]) == 0 || bytes.Equal(fields["index"], []byte("null")) || json.Unmarshal(fields["index"], &index) != nil || index < 0 || index >= maxAgentCalls || index > len(*s) || seen[index] {
			return false
		}
		seen[index] = true
		fresh := index == len(*s)
		if fresh {
			*s = append(*s, streamedTool{})
		}
		call := &(*s)[index]
		var id, kind string
		if v, ok := fields["id"]; ok {
			if json.Unmarshal(v, &id) != nil || !identifier(id) || (!fresh && id != call.id) {
				return false
			}
			call.id = id
		} else if fresh {
			return false
		}
		if v, ok := fields["type"]; ok {
			if json.Unmarshal(v, &kind) != nil || kind != "function" {
				return false
			}
		} else if fresh {
			return false
		}
		var function map[string]json.RawMessage
		if json.Unmarshal(fields["function"], &function) != nil || function == nil {
			return false
		}
		for k := range function {
			if k != "name" && k != "arguments" {
				return false
			}
		}
		if v, ok := function["name"]; ok {
			var name string
			if json.Unmarshal(v, &name) != nil || !identifier(name) || (!fresh && name != call.name) {
				return false
			}
			call.name = name
		} else if fresh {
			return false
		}
		if v, ok := function["arguments"]; ok {
			var part string
			if bytes.Equal(v, []byte("null")) || json.Unmarshal(v, &part) != nil || len(part) > maxAgentArguments-len(call.arguments) {
				return false
			}
			call.arguments = append(call.arguments, part...)
		}
	}
	return true
}

func (s streamedTools) complete() ([]providers.ToolCall, error) {
	out := make([]providers.ToolCall, 0, len(s))
	ids := map[string]bool{}
	for _, c := range s {
		if ids[c.id] || !uniqueJSON(c.arguments) {
			return nil, ErrProjection
		}
		ids[c.id] = true
		var object map[string]json.RawMessage
		if json.Unmarshal(c.arguments, &object) != nil || object == nil {
			return nil, ErrProjection
		}
		// Preserve numeric spelling and nested values; duplicate keys at every
		// depth have already been rejected before any runtime tool is registered.
		arguments, err := json.Marshal(object)
		if err != nil || len(arguments) > maxAgentArguments {
			return nil, ErrProjection
		}
		out = append(out, providers.ToolCall{ID: c.id, Name: c.name, Arguments: arguments})
	}
	return out, nil
}

// Go accepts case-folded struct keys; the native JavaScript client does not.
// Require canonical wire keys before forwarding to avoid two interpretations.
func agentChunkFields(data []byte) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return false
	}
	for key := range fields {
		switch key {
		case "id", "object", "model", "choices", "usage", "created", "system_fingerprint", "service_tier":
		default:
			return false
		}
	}
	var choices []map[string]json.RawMessage
	if json.Unmarshal(fields["choices"], &choices) != nil {
		return false
	}
	for _, choice := range choices {
		for key := range choice {
			if key != "index" && key != "delta" && key != "finish_reason" && key != "logprobs" {
				return false
			}
		}
	}
	if raw := fields["usage"]; len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		var usage map[string]json.RawMessage
		if json.Unmarshal(raw, &usage) != nil {
			return false
		}
		for key := range usage {
			switch key {
			case "prompt_tokens", "completion_tokens", "total_tokens", "prompt_tokens_details", "completion_tokens_details":
			default:
				return false
			}
		}
	}
	return true
}
