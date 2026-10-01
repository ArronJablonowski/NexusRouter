package pi

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// startGateway exposes only one authenticated completion operation to the
// isolated child. All upstream traffic uses the host policy transport. The
// opaque child token is distinct from the provider credential.
func startGateway(ctx context.Context, c Config) (base, key string, closeGateway func(), err error) {
	target, err := url.Parse(c.BaseURL)
	if err != nil {
		return "", "", nil, ErrProtocol
	}
	target.Path = strings.TrimRight(target.Path, "/") + "/chat/completions"
	target.RawPath = ""
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return "", "", nil, ErrRun
	}
	var handlers sync.WaitGroup
	var lifecycle sync.Mutex
	closed := false
	token := rand.Text()
	var dispatched atomic.Bool
	client := &http.Client{Transport: c.Transport, Timeout: c.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lifecycle.Lock()
		if closed {
			lifecycle.Unlock()
			http.Error(w, "gateway closed", http.StatusServiceUnavailable)
			return
		}
		handlers.Add(1)
		lifecycle.Unlock()
		defer handlers.Done()
		deny := func(code int) { http.Error(w, "harness request denied", code) }
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" || r.URL.RawQuery != "" || r.URL.RawPath != "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			deny(http.StatusForbidden)
			return
		}
		body, e := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxRecordBytes))
		if e != nil || !validGatewayRequest(body, c.Model, c.MaxOutputTokens) {
			deny(http.StatusBadRequest)
			return
		}
		// Normalize nested JSON through the same parser used for validation so
		// upstream parsers cannot reinterpret duplicate nested fields.
		var canonical any
		d := json.NewDecoder(bytes.NewReader(body))
		d.UseNumber()
		if d.Decode(&canonical) != nil {
			deny(http.StatusBadRequest)
			return
		}
		if len(c.Messages) > 0 {
			contextBody, contextErr := contextMessages(c.Messages)
			if contextErr != nil {
				deny(http.StatusBadRequest)
				return
			}
			canonical.(map[string]any)["messages"] = json.RawMessage(contextBody)
		}
		body, e = json.Marshal(canonical)
		if e != nil || len(body) > MaxRecordBytes || !validGatewayRequest(body, c.Model, c.MaxOutputTokens) {
			deny(http.StatusBadRequest)
			return
		}
		if !dispatched.CompareAndSwap(false, true) {
			deny(http.StatusConflict)
			return
		}
		upstream, cancel := context.WithCancel(ctx)
		defer cancel()
		stop := context.AfterFunc(r.Context(), cancel)
		defer stop()
		request, e := http.NewRequestWithContext(upstream, http.MethodPost, target.String(), bytes.NewReader(body))
		if e != nil {
			deny(http.StatusBadGateway)
			return
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "text/event-stream")
		if c.APIKey != "" {
			request.Header.Set("Authorization", "Bearer "+c.APIKey)
		}
		response, e := client.Do(request)
		if e != nil {
			deny(http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
			deny(http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		// The RPC reader has independent framing/output caps. Bound the raw provider
		// stream too; truncation cannot manufacture the native completion marker.
		_, _ = io.CopyN(flushWriter{w}, response.Body, 16<<20)
	})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: c.Timeout, WriteTimeout: c.Timeout, MaxHeaderBytes: 8192, ErrorLog: log.New(io.Discard, "", 0)}
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(listener) }()
	return "http://" + listener.Addr().String() + "/v1", token, func() {
		lifecycle.Lock()
		closed = true
		lifecycle.Unlock()
		_ = server.Close()
		<-done
		handlers.Wait()
	}, nil
}

type flushWriter struct{ http.ResponseWriter }

func (w flushWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
	return n, err
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
