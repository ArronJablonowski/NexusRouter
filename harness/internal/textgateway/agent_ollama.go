package textgateway

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// Native Ollama tools carry object arguments and need not provide call IDs.
// Each verified turn receives fresh host IDs before canonical SSE delivery. A
// native stop with proposals means tool_calls at the OpenAI harness boundary.
func ollamaAgentCompletion(reader io.Reader, model string) (Completion, error) {
	bad := func() (Completion, error) { return Completion{}, ErrProjection }
	limited := &io.LimitedReader{R: reader, N: maxStreamBytes + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 4096), MaxRecordBytes)
	var wire bytes.Buffer
	prefix := "nexus-ollama-" + rand.Text()
	calls, textBytes, records := 0, 0, 0
	done := false
	nativeIDs := map[string]bool{}
	write := func(delta any, finish any, usage any) bool {
		chunk := map[string]any{"id": prefix, "object": "chat.completion.chunk", "model": model, "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}
		if usage != nil {
			chunk["usage"] = usage
		}
		body, e := json.Marshal(chunk)
		if e != nil || len(body) > MaxRecordBytes {
			return false
		}
		fmt.Fprintf(&wire, "data: %s\n\n", body)
		return wire.Len() <= maxStreamBytes
	}
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		records++
		if done || records > 10000 || !uniqueJSON(line) {
			return bad()
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(line, &fields) != nil || fields == nil {
			return bad()
		}
		for key := range fields {
			switch key {
			case "model", "created_at", "message", "done", "done_reason", "total_duration", "load_duration", "prompt_eval_count", "prompt_eval_duration", "eval_count", "eval_duration":
			default:
				return bad()
			}
		}
		var record struct {
			Model   string          `json:"model"`
			Done    bool            `json:"done"`
			Reason  string          `json:"done_reason"`
			Message json.RawMessage `json:"message"`
			Input   *int64          `json:"prompt_eval_count"`
			Output  *int64          `json:"eval_count"`
		}
		if json.Unmarshal(line, &record) != nil || record.Model != model {
			return bad()
		}
		if len(record.Message) > 0 {
			var message map[string]json.RawMessage
			if json.Unmarshal(record.Message, &message) != nil || message == nil {
				return bad()
			}
			for key := range message {
				switch key {
				case "role", "content", "tool_calls", "thinking", "images":
				default:
					return bad()
				}
			}
			var m struct {
				Role, Content, Thinking string
				Calls                   []json.RawMessage `json:"tool_calls"`
				Images                  json.RawMessage
			}
			if json.Unmarshal(record.Message, &m) != nil || (m.Role != "" && m.Role != "assistant") || m.Thinking != "" || nonemptyArray(m.Images) {
				return bad()
			}
			textBytes += len(m.Content)
			if textBytes > MaxTextBytes {
				return bad()
			}
			text := m.Content
			for len(text) > 0 {
				n := len(text)
				if n > 32768 {
					n = 32768
					for !utf8.RuneStart(text[n]) {
						n--
					}
				}
				if !write(map[string]string{"role": "assistant", "content": text[:n]}, nil, nil) {
					return bad()
				}
				text = text[n:]
			}
			for _, raw := range m.Calls {
				if calls >= maxAgentCalls {
					return bad()
				}
				var call map[string]json.RawMessage
				if json.Unmarshal(raw, &call) != nil || call["function"] == nil {
					return bad()
				}
				for key := range call {
					if key != "function" && key != "type" && key != "id" {
						return bad()
					}
				}
				if raw, ok := call["type"]; ok {
					var kind string
					if json.Unmarshal(raw, &kind) != nil || kind != "function" {
						return bad()
					}
				}
				if raw, ok := call["id"]; ok {
					var id string
					if json.Unmarshal(raw, &id) != nil || !identifier(id) || nativeIDs[id] {
						return bad()
					}
					nativeIDs[id] = true
				}
				var function map[string]json.RawMessage
				if json.Unmarshal(call["function"], &function) != nil {
					return bad()
				}
				for key := range function {
					if key != "name" && key != "arguments" && key != "index" {
						return bad()
					}
				}
				var name string
				if json.Unmarshal(function["name"], &name) != nil || !agentToolName(name) || len(function["arguments"]) > maxAgentArguments {
					return bad()
				}
				var args map[string]json.RawMessage
				if json.Unmarshal(function["arguments"], &args) != nil || args == nil {
					return bad()
				}
				if raw, ok := function["index"]; ok {
					var index int
					if string(raw) == "null" || json.Unmarshal(raw, &index) != nil || index < 0 || index >= maxAgentCalls {
						return bad()
					}
				}
				delta := map[string]any{"tool_calls": []any{map[string]any{"index": calls, "id": fmt.Sprintf("%s-%d", prefix, calls), "type": "function", "function": map[string]string{"name": name, "arguments": string(function["arguments"])}}}}
				if !write(delta, nil, nil) {
					return bad()
				}
				calls++
			}
		}
		if record.Done {
			if record.Reason != "stop" || (record.Input == nil) != (record.Output == nil) {
				return bad()
			}
			var usage any
			if record.Input != nil {
				if *record.Input < 0 || *record.Output < 0 || *record.Input > 1<<40 || *record.Output > 1<<40 {
					return bad()
				}
				usage = map[string]int64{"prompt_tokens": *record.Input, "completion_tokens": *record.Output, "total_tokens": *record.Input + *record.Output}
			} else if _, ok := fields["prompt_eval_count"]; ok {
				return bad()
			} else if _, ok := fields["eval_count"]; ok {
				return bad()
			}
			finish := "stop"
			if calls > 0 {
				finish = "tool_calls"
			}
			if !write(map[string]string{}, finish, usage) {
				return bad()
			}
			done = true
		} else if record.Reason != "" || record.Input != nil || record.Output != nil {
			return bad()
		}
	}
	if scanner.Err() != nil || limited.N <= 0 || !done {
		return bad()
	}
	wire.WriteString("data: [DONE]\n\n")
	return VerifyAgentCompletion(strings.NewReader(wire.String()), model)
}
