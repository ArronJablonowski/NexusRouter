package pi

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/ArronJablonowski/NexusRouter/harness/internal/wirejson"
)

type agentRPCCall struct {
	name                           string
	started, ended, result, failed bool
}

// AgentProtocol validates the pinned native process lifecycle, not execution or
// quality. The host gateway/journal independently verifies provider output and
// tool effects. Never register or execute a tool from these child events.
type AgentProtocol struct {
	base                              *Protocol
	maxTurns, turns, records          int
	active, assistant, ended, settled bool
	tools                             map[string]bool
	seen                              map[string]bool
	calls                             map[string]*agentRPCCall
	order                             []string
	next                              int
	final                             *Result
}

func NewAgentProtocol(provider, model string, maxTurns int, tools []string) (*AgentProtocol, error) {
	base, e := NewProtocol(provider, model)
	if e != nil || maxTurns < 1 || maxTurns > 64 || len(tools) < 1 || len(tools) > 128 {
		return nil, ErrProtocol
	}
	names := map[string]bool{}
	for _, name := range tools {
		if name == "" || names[name] {
			return nil, ErrProtocol
		}
		names[name] = true
	}
	return &AgentProtocol{base: base, maxTurns: maxTurns, tools: names, seen: map[string]bool{}}, nil
}
func (p *AgentProtocol) Ready() bool { return p != nil && p.base.Ready() }
func (p *AgentProtocol) Result() (Result, error) {
	if p == nil || !p.settled || p.final == nil {
		return Result{}, ErrRun
	}
	return *p.final, nil
}
func (p *AgentProtocol) Consume(line []byte) (bool, error) {
	if p == nil || p.settled || len(line) == 0 || len(line) > MaxRecordBytes || !utf8.Valid(line) || !wirejson.Unique(line) || p.records >= MaxRecords {
		return false, ErrProtocol
	}
	p.records++
	var event struct {
		Type, ToolCallID, ToolName, ParentToolCallID string
		Message, AssistantMessageEvent               json.RawMessage
		WillRetry, IsError                           *bool
	}
	if json.Unmarshal(line, &event) != nil {
		return false, ErrProtocol
	}
	switch event.Type {
	case "response", "agent_start":
		if p.ended {
			return false, ErrProtocol
		}
		return p.base.Consume(line)
	case "turn_start":
		if !p.base.started || p.ended || p.active || p.final != nil || p.turns >= p.maxTurns {
			return false, ErrProtocol
		}
		p.turns++
		p.active = true
		p.assistant = false
		p.calls = map[string]*agentRPCCall{}
		p.order = nil
		p.next = 0
	case "message_start":
		if !p.active || p.ended {
			return false, ErrProtocol
		}
	case "message_update":
		if !p.active || p.assistant || p.ended {
			return false, ErrProtocol
		}
		var update struct{ Type string }
		if json.Unmarshal(event.AssistantMessageEvent, &update) != nil {
			return false, ErrProtocol
		}
		switch update.Type {
		case "start", "text_start", "text_delta", "text_end", "thinking_start", "thinking_delta", "thinking_end", "toolcall_start", "toolcall_delta", "toolcall_end":
		default:
			return false, ErrProtocol
		}
	case "message_end":
		if !p.active || p.ended {
			return false, ErrProtocol
		}
		var message struct {
			Role, Provider, Model, ResponseModel, StopReason, ToolCallID, ToolName string
			Content                                                                json.RawMessage
			Usage                                                                  *Usage
			IsError                                                                *bool
		}
		if json.Unmarshal(event.Message, &message) != nil {
			return false, ErrProtocol
		}
		switch message.Role {
		case "user", "system":
			return false, nil
		case "toolResult":
			call := p.calls[message.ToolCallID]
			if call == nil || !call.ended || call.result || message.ToolName != call.name || message.IsError == nil || *message.IsError != call.failed {
				return false, ErrProtocol
			}
			call.result = true
		case "assistant":
			if p.assistant || message.Provider != p.base.provider || message.Model != p.base.model || (message.ResponseModel != "" && message.ResponseModel != p.base.model) || !message.Usage.valid() {
				return false, ErrProtocol
			}
			var blocks []struct {
				Type, ID, Name, Text string
				Arguments            json.RawMessage
			}
			if json.Unmarshal(message.Content, &blocks) != nil {
				return false, ErrProtocol
			}
			var text strings.Builder
			for _, block := range blocks {
				switch block.Type {
				case "text":
					if text.Len()+len(block.Text) > MaxOutputBytes {
						return false, ErrProtocol
					}
					text.WriteString(block.Text)
				case "thinking":
				case "toolCall":
					if message.StopReason != "toolUse" || !p.tools[block.Name] || block.ID == "" || len(block.ID) > 256 || strings.ContainsAny(block.ID, "/ \t\r\n") || p.seen[block.ID] || len(p.seen) >= 128 || len(block.Arguments) > 64<<10 || !wirejson.Unique(block.Arguments) {
						return false, ErrProtocol
					}
					var args map[string]json.RawMessage
					if json.Unmarshal(block.Arguments, &args) != nil || args == nil {
						return false, ErrProtocol
					}
					p.seen[block.ID] = true
					p.calls[block.ID] = &agentRPCCall{name: block.Name}
					p.order = append(p.order, block.ID)
				default:
					return false, ErrProtocol
				}
			}
			switch message.StopReason {
			case "toolUse":
				if len(p.calls) == 0 {
					return false, ErrProtocol
				}
			case "stop":
				if len(p.calls) != 0 || strings.TrimSpace(text.String()) == "" {
					return false, ErrRun
				}
				p.final = &Result{Provider: message.Provider, Model: message.Model, Text: text.String(), Usage: message.Usage}
			default:
				return false, ErrRun
			}
			p.assistant = true
		default:
			return false, ErrProtocol
		}
	case "tool_execution_start", "tool_execution_update", "tool_execution_end":
		if !p.active || !p.assistant || p.final != nil || event.ParentToolCallID != "" {
			return false, ErrProtocol
		}
		call := p.calls[event.ToolCallID]
		if call == nil || call.name != event.ToolName || call.ended {
			return false, ErrProtocol
		}
		switch event.Type {
		case "tool_execution_start":
			if call.started || p.next >= len(p.order) || event.ToolCallID != p.order[p.next] {
				return false, ErrProtocol
			}
			call.started = true
		case "tool_execution_update":
			if !call.started {
				return false, ErrProtocol
			}
		case "tool_execution_end":
			if !call.started || event.IsError == nil {
				return false, ErrProtocol
			}
			call.ended = true
			call.failed = *event.IsError
			p.next++
		}
	case "turn_end":
		if !p.active || !p.assistant || p.ended {
			return false, ErrProtocol
		}
		for _, c := range p.calls {
			if !c.started || !c.ended || !c.result {
				return false, ErrProtocol
			}
		}
		p.active = false
	case "agent_end":
		if !p.base.started || p.active || p.ended || p.final == nil || event.WillRetry == nil || *event.WillRetry {
			return false, ErrRun
		}
		p.ended = true
	case "agent_settled":
		if !p.base.accepted || !p.ended || p.final == nil {
			return false, ErrProtocol
		}
		p.settled = true
		return true, nil
	default:
		return false, ErrProtocol
	}
	return false, nil
}
