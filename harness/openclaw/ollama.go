package openclaw

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

func ollamaRequest(body []byte, c gatewayConfig) ([]byte, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		return nil, ErrProjection
	}
	var requested struct {
		MaxTokens           int `json:"max_tokens"`
		MaxCompletionTokens int `json:"max_completion_tokens"`
	}
	if json.Unmarshal(body, &requested) != nil {
		return nil, ErrProjection
	}
	limit := requested.MaxTokens
	if requested.MaxCompletionTokens > 0 {
		if limit > 0 && limit != requested.MaxCompletionTokens {
			return nil, ErrProjection
		}
		limit = requested.MaxCompletionTokens
	}
	if limit < 1 || limit > c.MaxOutputTokens {
		return nil, ErrProjection
	}
	options := map[string]any{"num_ctx": c.ContextTokens, "num_predict": limit}
	for key, value := range fields {
		switch key {
		case "model", "messages", "stream", "stream_options", "max_tokens", "max_completion_tokens", "store":
		case "temperature", "top_p", "seed", "stop":
			options[key] = value
		case "reasoning_effort":
			var effort string
			if json.Unmarshal(value, &effort) != nil || effort != "none" {
				return nil, ErrProjection
			}
		default:
			return nil, ErrProjection
		}
	}
	var messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(fields["messages"], &messages) != nil {
		return nil, ErrProjection
	}
	type message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	translated := make([]message, 0, len(messages))
	for _, m := range messages {
		if m.Role != "system" && m.Role != "user" && m.Role != "assistant" {
			return nil, ErrProjection
		}
		var text string
		if json.Unmarshal(m.Content, &text) != nil {
			var blocks []struct{ Type, Text string }
			if json.Unmarshal(m.Content, &blocks) != nil {
				return nil, ErrProjection
			}
			for _, b := range blocks {
				if b.Type != "text" {
					return nil, ErrProjection
				}
				text += b.Text
			}
		}
		translated = append(translated, message{m.Role, text})
	}
	return json.Marshal(struct {
		Model    string         `json:"model"`
		Messages []message      `json:"messages"`
		Stream   bool           `json:"stream"`
		Think    bool           `json:"think"`
		Options  map[string]any `json:"options"`
	}{c.Model, translated, true, false, options})
}

// Read the bounded native stream through its terminal record and EOF before
// returning any successful OpenAI framing. The harness may normalize a truncated SSE
// stream, so an omitted native terminal must never look like successful output.
func ollamaCompletion(reader io.Reader, model string) ([]byte, error) {
	limited := &io.LimitedReader{R: reader, N: 16<<20 + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 4096), MaxRecordBytes)
	var text bytes.Buffer
	done := false
	var input, output *int64
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if done {
			return nil, ErrProjection
		}
		var record struct {
			Model   string
			Error   string
			Message struct {
				Role, Content, Thinking string
				ToolCalls               json.RawMessage `json:"tool_calls"`
				Images                  json.RawMessage
			}
			Done   bool
			Reason string `json:"done_reason"`
			Input  *int64 `json:"prompt_eval_count"`
			Output *int64 `json:"eval_count"`
		}
		if !uniqueJSON(line) || json.Unmarshal(line, &record) != nil || record.Error != "" || record.Model != model || record.Message.Thinking != "" || nonemptyArray(record.Message.ToolCalls) || nonemptyArray(record.Message.Images) || (record.Message.Role != "" && record.Message.Role != "assistant") {
			return nil, ErrProjection
		}
		if text.Len()+len(record.Message.Content) > MaxRecordBytes/2 {
			return nil, ErrProjection
		}
		text.WriteString(record.Message.Content)
		if record.Done {
			if record.Reason != "stop" {
				return nil, ErrProjection
			}
			done = true
			input, output = record.Input, record.Output
		} else if record.Reason != "" {
			return nil, ErrProjection
		}
	}
	if scanner.Err() != nil || limited.N <= 0 || !done {
		return nil, ErrProjection
	}
	first := map[string]any{"id": "nexus-ollama", "object": "chat.completion.chunk", "model": model, "choices": []any{map[string]any{"index": 0, "delta": map[string]string{"role": "assistant", "content": text.String()}, "finish_reason": nil}}}
	final := map[string]any{"id": "nexus-ollama", "object": "chat.completion.chunk", "model": model, "choices": []any{map[string]any{"index": 0, "delta": map[string]string{}, "finish_reason": "stop"}}}
	if (input == nil) != (output == nil) {
		return nil, ErrProjection
	}
	if input != nil {
		if *input < 0 || *output < 0 || *input > 1<<40 || *output > 1<<40 {
			return nil, ErrProjection
		}
		final["usage"] = map[string]int64{"prompt_tokens": *input, "completion_tokens": *output, "total_tokens": *input + *output}
	}
	a, e := json.Marshal(first)
	if e != nil {
		return nil, e
	}
	b, e := json.Marshal(final)
	if e != nil {
		return nil, e
	}
	return []byte(fmt.Sprintf("data: %s\n\ndata: %s\n\ndata: [DONE]\n\n", a, b)), nil
}

func nonemptyArray(raw json.RawMessage) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	var entries []json.RawMessage
	return json.Unmarshal(raw, &entries) != nil || len(entries) > 0
}
