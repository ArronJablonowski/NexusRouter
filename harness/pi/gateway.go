package pi

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/ArronJablonowski/NexusRouter/harness/internal/textgateway"
	"io"
)

func startVerifiedGateway(ctx context.Context, c Config) (string, string, func() (textgateway.Completion, error), func(), error) {
	return textgateway.Start(ctx, textgateway.Config{UpstreamProtocol: c.UpstreamProtocol, BaseURL: c.BaseURL, APIKey: c.APIKey, Model: c.Model, ContextTokens: c.ContextTokens, MaxOutputTokens: c.MaxOutputTokens, Timeout: c.Timeout, Transport: c.Transport, Messages: c.Messages})
}

func startGateway(ctx context.Context, c Config) (string, string, func(), error) {
	base, key, _, closeGateway, err := startVerifiedGateway(ctx, c)
	return base, key, closeGateway, err
}

func validGatewayRequest(body []byte, model string, limit int) bool {
	// Reject duplicate top-level keys rather than relying on parser-specific last
	// wins behavior at the upstream boundary.
	decoder := json.NewDecoder(bytes.NewReader(body))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return false
	}
	fields := map[string]json.RawMessage{}
	for decoder.More() {
		key, e := decoder.Token()
		name, ok := key.(string)
		if e != nil || !ok || fields[name] != nil {
			return false
		}
		var raw json.RawMessage
		if decoder.Decode(&raw) != nil {
			return false
		}
		fields[name] = raw
	}
	if _, err = decoder.Token(); err != nil {
		return false
	}
	if _, err = decoder.Token(); err != io.EOF {
		return false
	}
	for key := range fields {
		switch key {
		case "model", "messages", "stream", "stream_options", "max_tokens", "max_completion_tokens", "temperature", "top_p", "frequency_penalty", "presence_penalty", "stop", "reasoning_effort", "seed", "store":
		default:
			return false
		}
	}
	var request struct {
		Model               string
		Stream              bool
		MaxTokens           int `json:"max_tokens"`
		MaxCompletionTokens int `json:"max_completion_tokens"`
		Messages            []struct {
			Role    string
			Content json.RawMessage
		}
	}
	if json.Unmarshal(body, &request) != nil || request.Model != model || !request.Stream || request.MaxTokens < 0 || request.MaxCompletionTokens < 0 || request.MaxTokens > limit || request.MaxCompletionTokens > limit || request.MaxTokens+request.MaxCompletionTokens == 0 || len(request.Messages) == 0 {
		return false
	}
	var messages []map[string]json.RawMessage
	if json.Unmarshal(fields["messages"], &messages) != nil {
		return false
	}
	for _, message := range messages {
		for key := range message {
			if key != "role" && key != "content" {
				return false
			}
		}
	}
	if raw, ok := fields["store"]; ok {
		var store bool
		if json.Unmarshal(raw, &store) != nil || store {
			return false
		}
	}
	for _, message := range request.Messages {
		if message.Role != "system" && message.Role != "user" && message.Role != "assistant" && message.Role != "developer" {
			return false
		}
		var text string
		if len(message.Content) > 0 && message.Content[0] == '"' && json.Unmarshal(message.Content, &text) == nil {
			continue
		}
		var rawBlocks []map[string]json.RawMessage
		if json.Unmarshal(message.Content, &rawBlocks) != nil {
			return false
		}
		for _, block := range rawBlocks {
			for key := range block {
				if key != "type" && key != "text" {
					return false
				}
			}
		}
		var blocks []struct{ Type, Text string }
		if json.Unmarshal(message.Content, &blocks) != nil || len(blocks) == 0 {
			return false
		}
		for _, block := range blocks {
			if block.Type != "text" {
				return false
			}
		}
	}
	return true
}
