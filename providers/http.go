package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type HTTP struct {
	base   string
	kind   string
	key    string
	client *http.Client
}

const defaultRequestTimeout = 5 * time.Minute

// NewHTTP accepts an owned transport so the application can enforce egress.
// Redirects are rejected to avoid forwarding prompts or credentials elsewhere.
func NewHTTP(base, kind, key string, transport http.RoundTripper) (*HTTP, error) {
	return NewHTTPWithTimeout(base, kind, key, transport, defaultRequestTimeout)
}

// NewHTTPWithTimeout constructs an adapter with a bounded total request
// lifetime. Callers may still supply a shorter context deadline.
func NewHTTPWithTimeout(base, kind, key string, transport http.RoundTripper, timeout time.Duration) (*HTTP, error) {
	u, err := url.Parse(base)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid provider endpoint")
	}
	if kind != "ollama" && kind != "openai_compatible" {
		return nil, errors.New("unsupported provider kind")
	}
	if transport == nil {
		return nil, errors.New("explicit provider transport required")
	}
	if timeout < 100*time.Millisecond || timeout > defaultRequestTimeout {
		return nil, errors.New("invalid provider request timeout")
	}
	return &HTTP{strings.TrimRight(base, "/"), kind, key, &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (p *HTTP) send(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, &Failure{Code: "invalid_request"}
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.base+path, reader)
	if err != nil {
		return nil, &Failure{Code: "invalid_request"}
	}
	req.Header.Set("Content-Type", "application/json")
	if p.key != "" {
		req.Header.Set("Authorization", "Bearer "+p.key)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &Failure{Code: "transport", Retryable: true}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4097))
		resp.Body.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		code := "http_error"
		retry := false
		switch {
		case resp.StatusCode == 401 || resp.StatusCode == 403:
			code = "authentication"
		case resp.StatusCode == 429:
			code = "rate_limit"
			retry = true
		case resp.StatusCode == http.StatusRequestEntityTooLarge:
			code = "context_overflow"
		case resp.StatusCode == http.StatusBadRequest:
			code = "invalid_request"
			if len(body) <= 4096 && p.kind == "openai_compatible" && openAIContextOverflow(body) {
				code = "context_overflow"
			}
		case resp.StatusCode >= 500:
			code = "unavailable"
			retry = true
		}
		return nil, &Failure{Code: code, Retryable: retry}
	}
	return resp, nil
}

func openAIContextOverflow(body []byte) bool {
	var envelope struct {
		Error *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	return json.Unmarshal(body, &envelope) == nil && envelope.Error != nil && envelope.Error.Code == "context_length_exceeded"
}

func (p *HTTP) Models(ctx context.Context) ([]string, error) {
	path := "/models"
	if p.kind == "ollama" {
		path = "/api/tags"
	}
	resp, err := p.send(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20+1))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil || len(b) > 1<<20 {
		return nil, &Failure{Code: "invalid_response"}
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(b, &envelope) != nil || envelope == nil {
		return nil, &Failure{Code: "invalid_response"}
	}
	field, identity := "data", "id"
	if p.kind == "ollama" {
		field, identity = "models", "name"
	}
	// A missing list, null, or an error envelope is not a healthy empty pool.
	var list []map[string]json.RawMessage
	if json.Unmarshal(envelope[field], &list) != nil || list == nil || (len(envelope["error"]) > 0 && string(envelope["error"]) != "null") {
		return nil, &Failure{Code: "invalid_response"}
	}
	ids := []string{}
	seen := map[string]bool{}
	for _, m := range list {
		var id string
		if json.Unmarshal(m[identity], &id) != nil || strings.TrimSpace(id) == "" {
			return nil, &Failure{Code: "invalid_response"}
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (p *HTTP) Stream(ctx context.Context, r Request, emit func(Chunk) error) error {
	if r.Model == "" || len(r.Messages) == 0 || emit == nil || !validMaxOutputTokens(r.MaxOutputTokens) || r.ContextTokens < 0 || r.ContextTokens > MaxOutputTokens {
		return &Failure{Code: "invalid_request"}
	}
	if ValidateMessages(r.Messages) != nil {
		return &Failure{Code: "invalid_conversation"}
	}
	tools := []any{}
	toolNames := map[string]bool{}
	for _, t := range r.Tools {
		if t.Name == "" || toolNames[t.Name] || len(r.Tools) > 128 || !jsonObject(t.Parameters) {
			return &Failure{Code: "invalid_tool_schema"}
		}
		toolNames[t.Name] = true
		tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": t.Name, "description": t.Description, "parameters": t.Parameters}})
	}
	messages := []any{}
	callNames := map[string]string{}
	for _, m := range r.Messages {
		if m.Role != "system" && m.Role != "user" && m.Role != "assistant" && m.Role != "tool" {
			return &Failure{Code: "invalid_role"}
		}
		wire := map[string]any{"role": m.Role, "content": ToolResultContent(m)}
		if m.ToolCallID != "" {
			if p.kind == "ollama" {
				name, ok := callNames[m.ToolCallID]
				if !ok {
					return &Failure{Code: "unpaired_tool_result"}
				}
				wire["tool_name"] = name
			} else {
				wire["tool_call_id"] = m.ToolCallID
			}
		}
		calls := []any{}
		for _, c := range m.ToolCalls {
			if c.ID == "" || c.Name == "" || !jsonObject(c.Arguments) {
				return &Failure{Code: "invalid_tool_arguments"}
			}
			callNames[c.ID] = c.Name
			var args any = string(c.Arguments)
			if p.kind == "ollama" {
				args = c.Arguments
			}
			calls = append(calls, map[string]any{"id": c.ID, "type": "function", "function": map[string]any{"name": c.Name, "arguments": args}})
		}
		if len(calls) > 0 {
			wire["tool_calls"] = calls
		}
		messages = append(messages, wire)
	}
	body := map[string]any{"model": r.Model, "messages": messages, "stream": true}
	if len(tools) > 0 {
		body["tools"] = tools
	}
	path := "/chat/completions"
	if p.kind == "ollama" {
		path = "/api/chat"
		options := map[string]int64{}
		if r.MaxOutputTokens > 0 {
			options["num_predict"] = r.MaxOutputTokens
		}
		if r.ContextTokens > 0 {
			options["num_ctx"] = r.ContextTokens
		}
		if len(options) > 0 {
			body["options"] = options
		}
	} else {
		body["stream_options"] = map[string]bool{"include_usage": true}
		if r.MaxOutputTokens > 0 {
			body["max_tokens"] = r.MaxOutputTokens
		}
	}
	if len(r.JSONSchema) > 0 {
		if !jsonObject(r.JSONSchema) {
			return &Failure{Code: "invalid_schema"}
		}
		if p.kind == "ollama" {
			body["format"] = r.JSONSchema
		} else {
			body["response_format"] = map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "result", "strict": true, "schema": r.JSONSchema}}
		}
	}
	resp, err := p.send(ctx, "POST", path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if p.kind == "ollama" {
		err = readOllama(resp.Body, emit)
	} else {
		err = readSSE(resp.Body, emit)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}
