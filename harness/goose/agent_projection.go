package goose

import (
	"bufio"
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/ArronJablonowski/NexusRouter/harness/internal/wirejson"
	"github.com/ArronJablonowski/NexusRouter/providers"
)

type agentBlock struct {
	Type, ID string
	Text     *string
	ToolCall *struct {
		Status string
		Value  *struct {
			Name      string
			Arguments json.RawMessage
		}
	}
	ToolResult *struct {
		Status string
		Value  *struct {
			Content []struct{ Type, Text string }
			IsError *bool `json:"isError"`
		}
	}
}

// parseAgentProjection reconciles every native proposal and observation with
// the completed host transcript. Native token totals are never usage evidence.
func parseAgentProjection(body []byte, exit int, model string, transcript []providers.Message) (Projection, error) {
	bad := func() (Projection, error) { return Projection{}, ErrProjection }
	if exit != 0 || len(body) == 0 || len(body) > MaxStreamBytes || body[len(body)-1] != '\n' || !utf8.Valid(body) || len(transcript) == 0 {
		return bad()
	}
	var joinErr error
	body, joinErr = joinAssistantFragments(body)
	if joinErr != nil {
		return bad()
	}
	scan := bufio.NewScanner(bytes.NewReader(body))
	scan.Buffer(make([]byte, 4096), MaxRecordBytes)
	index := 0
	pending := map[string]providers.Message{}
	var final []byte
	finished := false
	var out Projection
	for scan.Scan() {
		line := scan.Bytes()
		if finished || !wirejson.Unique(line) {
			return bad()
		}
		var event struct {
			Type    string
			Message *struct {
				ID, Role string
				Content  []agentBlock
				Metadata struct {
					Inference *struct {
						Provider, RequestedModel string
						ResolvedModel            *string
					}
				}
			}
		}
		if json.Unmarshal(line, &event) != nil {
			return bad()
		}
		if event.Type == "complete" {
			if event.Message != nil || index != len(transcript) || len(pending) != 0 || len(final) == 0 {
				return bad()
			}
			combined := append(append(append([]byte(nil), final...), '\n'), line...)
			combined = append(combined, '\n')
			var e error
			out, e = ParseProjection(combined, exit, "openai", model)
			if e != nil {
				return bad()
			}
			finished = true
			continue
		}
		m := event.Message
		if event.Type != "message" || m == nil || !label(m.ID) || len(m.Content) == 0 || len(final) != 0 {
			return bad()
		}
		switch m.Role {
		case "assistant":
			if len(pending) != 0 || index >= len(transcript) || transcript[index].Role != "assistant" || m.Metadata.Inference == nil || m.Metadata.Inference.Provider != "openai" || m.Metadata.Inference.RequestedModel != model {
				return bad()
			}
			if resolved := m.Metadata.Inference.ResolvedModel; resolved != nil && *resolved != model {
				return bad()
			}
			expected := transcript[index]
			index++
			var text strings.Builder
			var calls []providers.ToolCall
			for _, b := range m.Content {
				switch b.Type {
				case "text":
					if b.Text == nil || b.ToolCall != nil || b.ToolResult != nil || text.Len()+len(*b.Text) > MaxTextBytes {
						return bad()
					}
					text.WriteString(*b.Text)
				case "toolRequest":
					if b.Text != nil || b.ToolResult != nil || b.ToolCall == nil || b.ToolCall.Status != "success" || b.ToolCall.Value == nil || len(calls) >= len(expected.ToolCalls) {
						return bad()
					}
					want := expected.ToolCalls[len(calls)]
					if b.ID != want.ID || b.ToolCall.Value.Name != "nexus__"+want.Name || !agentArgumentsEqual(b.ToolCall.Value.Arguments, want.Arguments) {
						return bad()
					}
					calls = append(calls, want)
				default:
					return bad()
				}
			}
			if text.String() != expected.Content || len(calls) != len(expected.ToolCalls) {
				return bad()
			}
			if len(calls) == 0 {
				if index != len(transcript) {
					return bad()
				}
				final = append([]byte(nil), line...)
			} else {
				for _, c := range calls {
					if index >= len(transcript) || transcript[index].Role != "tool" || transcript[index].ToolCallID != c.ID {
						return bad()
					}
					pending[c.ID] = transcript[index]
					index++
				}
			}
		case "user":
			if m.Metadata.Inference != nil {
				return bad()
			}
			for _, b := range m.Content {
				expected, ok := pending[b.ID]
				if !ok || b.Type != "toolResponse" || b.Text != nil || b.ToolCall != nil || b.ToolResult == nil || b.ToolResult.Status != "success" || b.ToolResult.Value == nil {
					return bad()
				}
				result := b.ToolResult.Value
				if result.IsError == nil || *result.IsError != expected.ToolFailed || len(result.Content) != 1 || result.Content[0].Type != "text" || result.Content[0].Text != expected.Content {
					return bad()
				}
				delete(pending, b.ID)
			}
		default:
			return bad()
		}
	}
	if scan.Err() != nil || !finished {
		return bad()
	}
	return out, nil
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
	// Keep number spelling/precision rather than decoding to float64.
	x, ok := decode(a)
	if !ok {
		return false
	}
	y, ok := decode(b)
	return ok && reflect.DeepEqual(x, y)
}
