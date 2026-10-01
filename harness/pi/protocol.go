// Package pi implements the pinned Pi RPC adapter's protocol boundary. Accepted
// prompts and intermediate agent_end events are never completion evidence.
package pi

import (
	"encoding/json"
	"errors"
	"github.com/ArronJablonowski/NexusRouter/harness"
	"strings"
	"unicode/utf8"
)

var ErrProtocol = errors.New("Pi RPC protocol or identity mismatch")
var ErrRun = errors.New("Pi run failed or was aborted")

const MaxRecordBytes = 1 << 20
const MaxOutputBytes = 4 << 20
const MaxRecords = 10000

// Result describes execution, not correctness. Quality requires a separately
// bound evaluation. Thinking, tool arguments and raw errors are not returned.
type Result struct {
	Provider, Model, Text string
	Identity              harness.Identity
	Usage                 *Usage
}

// Protocol validates one no-tool prompt in one isolated Pi session. Tool-bearing
// runs require a separately authorized adapter capability; they fail closed here.
type Protocol struct {
	expectURL                                           string
	expectContext, expectOutput                         int
	provider, model                                     string
	ready, accepted, started, turnEnded, ended, settled bool
	turns, records                                      int
	final                                               *Result
}

func NewProtocol(provider, model string) (*Protocol, error) {
	if provider == "" || model == "" || len(provider) > 256 || len(model) > 256 || !utf8.ValidString(provider) || !utf8.ValidString(model) {
		return nil, ErrProtocol
	}
	return &Protocol{provider: provider, model: model}, nil
}
func (p *Protocol) Ready() bool { return p != nil && p.ready }
func (p *Protocol) Result() (Result, error) {
	if p == nil || !p.settled || p.final == nil {
		return Result{}, ErrRun
	}
	return *p.final, nil
}

// Consume requires LF-framed JSON supplied by the process runner. It supports
// deltas for lifecycle validation but only completed assistant text is final.
func (p *Protocol) Consume(line []byte) (bool, error) {
	if p == nil || p.settled || len(line) == 0 || len(line) > MaxRecordBytes || !utf8.Valid(line) || p.records >= MaxRecords {
		return false, ErrProtocol
	}
	p.records++
	var record struct {
		Type, ID, Command     string
		Success               *bool
		Data                  json.RawMessage
		Message               json.RawMessage
		WillRetry             *bool
		AssistantMessageEvent json.RawMessage
	}
	if json.Unmarshal(line, &record) != nil {
		return false, ErrProtocol
	}
	switch record.Type {
	case "response":
		if record.Success == nil || !*record.Success {
			return false, ErrRun
		}
		switch record.ID {
		case "state":
			if record.Command != "get_state" || p.ready || p.started {
				return false, ErrProtocol
			}
			var state struct {
				Model struct {
					Provider, ID, BaseURL    string
					ContextWindow, MaxTokens int
				}
				IsStreaming, IsCompacting, AutoCompactionEnabled bool
				MessageCount, PendingMessageCount                int
			}
			if json.Unmarshal(record.Data, &state) != nil || state.Model.Provider != p.provider || state.Model.ID != p.model || state.IsStreaming || state.IsCompacting || state.AutoCompactionEnabled || state.MessageCount != 0 || state.PendingMessageCount != 0 {
				return false, ErrProtocol
			}
			if p.expectURL != "" && (state.Model.BaseURL != p.expectURL || state.Model.ContextWindow != p.expectContext || state.Model.MaxTokens != p.expectOutput) {
				return false, ErrProtocol
			}
			p.ready = true
		case "prompt":
			if record.Command != "prompt" || !p.ready || p.accepted {
				return false, ErrProtocol
			}
			var data struct{ Disposition string }
			if json.Unmarshal(record.Data, &data) != nil || data.Disposition != "started" {
				return false, ErrProtocol
			}
			p.accepted = true
		default:
			return false, ErrProtocol
		}
	case "agent_start":
		if !p.ready || p.started {
			return false, ErrProtocol
		}
		p.started = true
	case "turn_start":
		if !p.started || p.ended || p.turns != 0 {
			return false, ErrProtocol
		}
		p.turns++
	case "message_start":
		if !p.started || p.ended {
			return false, ErrProtocol
		}
	case "message_update":
		if !p.started || p.ended || p.final != nil {
			return false, ErrProtocol
		}
		var update struct{ Type string }
		if json.Unmarshal(record.AssistantMessageEvent, &update) != nil {
			return false, ErrProtocol
		}
		switch update.Type {
		case "start", "text_start", "text_delta", "text_end", "thinking_start", "thinking_delta", "thinking_end":
		default:
			return false, ErrProtocol
		}
	case "message_end":
		if !p.started || p.ended {
			return false, ErrProtocol
		}
		var message struct {
			Usage                                            *Usage
			Role, Provider, Model, ResponseModel, StopReason string
			Content                                          json.RawMessage
		}
		if json.Unmarshal(record.Message, &message) != nil {
			return false, ErrProtocol
		}
		if message.Role == "user" || message.Role == "system" {
			return false, nil
		}
		if message.Role != "assistant" || p.final != nil || message.Provider != p.provider || message.Model != p.model || (message.ResponseModel != "" && message.ResponseModel != p.model) {
			return false, ErrProtocol
		}
		if !message.Usage.valid() {
			return false, ErrProtocol
		}
		if message.StopReason != "stop" {
			return false, ErrRun
		}
		var blocks []struct{ Type, Text string }
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
			default:
				return false, ErrProtocol
			}
		}
		if strings.TrimSpace(text.String()) == "" {
			return false, ErrRun
		}
		p.final = &Result{Provider: message.Provider, Model: message.Model, Text: text.String(), Usage: message.Usage}
	case "turn_end":
		if p.turns != 1 || p.final == nil || p.turnEnded || p.ended {
			return false, ErrProtocol
		}
		p.turnEnded = true
	case "agent_end":
		if !p.started || !p.turnEnded || p.ended || p.final == nil || record.WillRetry == nil || *record.WillRetry {
			return false, ErrRun
		}
		p.ended = true
	case "agent_settled":
		if !p.accepted || !p.ended || p.final == nil {
			return false, ErrProtocol
		}
		p.settled = true
		return true, nil
	default:
		return false, ErrProtocol
	}
	return false, nil
}
