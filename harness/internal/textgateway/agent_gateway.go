package textgateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/harness/toolbridge"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

// AgentConfig is host-only. The session must belong to this exact attribution,
// use the normal fenced/redacting journal and scoped executor, and remain alive
// until Close joins the gateway. Initial context and tool schemas are copied.
// Endpoint authorization, resource admission and approvals remain host policy.
type AgentConfig struct {
	Config
	Actual  harness.Identity
	Session *runtime.HarnessAgentSession
	Tools   []providers.Tool
}

// AgentGateway owns the conversation and rendezvous for one native run. Child
// conversation, schema and sampling overrides never become upstream authority.
// Retries of a child request are rejected; cached tool results live in Bridge.
type AgentGateway struct {
	BaseURL, Token, ToolToken string
	ctx                       context.Context
	cancel                    context.CancelFunc
	config                    AgentConfig
	target                    string
	client                    *http.Client
	bridge                    *toolbridge.Bridge
	server                    *http.Server
	done                      chan struct{}
	lifecycle                 sync.Mutex
	closed                    bool
	handlers                  sync.WaitGroup
	closeOnce                 sync.Once
	mu                        sync.Mutex
	messages                  []providers.Message
	tools                     json.RawMessage
	pending                   int
	ended                     bool
	final                     *Completion
	fault                     error
	requests                  map[[32]byte]bool
}

func StartAgent(ctx context.Context, c AgentConfig) (*AgentGateway, error) {
	if ctx == nil || ctx.Err() != nil || c.Session == nil || c.Actual.Validate() != nil || c.Actual.Model != c.Model || len(c.Tools) == 0 || len(c.Tools) > 128 || c.Transport == nil || c.Timeout <= 0 || c.Timeout > 15*time.Minute || c.ContextTokens < 8192 || c.MaxOutputTokens < 1 || c.MaxOutputTokens > 65536 || c.MaxOutputTokens >= c.ContextTokens || (c.UpstreamProtocol != "" && c.UpstreamProtocol != "openai_compatible" && c.UpstreamProtocol != "ollama") {
		return nil, ErrProjection
	}
	switch reflect.ValueOf(c.Transport).Kind() {
	case reflect.Pointer, reflect.Func, reflect.Map, reflect.Slice, reflect.Chan, reflect.Interface:
		if reflect.ValueOf(c.Transport).IsNil() {
			return nil, ErrProjection
		}
	}
	target, e := url.Parse(c.BaseURL)
	if e != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" || target.User != nil || target.RawQuery != "" || target.Fragment != "" {
		return nil, ErrProjection
	}
	suffix := "/chat/completions"
	if c.UpstreamProtocol == "ollama" {
		suffix = "/api/chat"
	}
	target.Path = strings.TrimRight(target.Path, "/") + suffix
	target.RawPath = ""
	messages, e := copyAgentMessages(c.Messages)
	if e != nil {
		return nil, e
	}
	schemas, e := agentSchemas(c.Tools)
	if e != nil {
		return nil, e
	}
	run, cancel := context.WithCancel(ctx)
	g := &AgentGateway{ctx: run, cancel: cancel, config: c, target: target.String(), messages: messages, tools: schemas, Token: rand.Text(), requests: map[[32]byte]bool{}, done: make(chan struct{})}
	if _, e = g.requestBody(); e != nil {
		cancel()
		return nil, e
	}
	g.client = &http.Client{Transport: c.Transport, Timeout: c.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	g.bridge, e = toolbridge.New(run, 128, c.Timeout, g.invoke)
	if e != nil {
		cancel()
		return nil, e
	}
	g.ToolToken = g.bridge.Token()
	listener, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		g.bridge.Close()
		cancel()
		return nil, e
	}
	g.BaseURL = "http://" + listener.Addr().String() + "/v1"
	g.server = &http.Server{Handler: http.HandlerFunc(g.serve), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: c.Timeout, WriteTimeout: c.Timeout, MaxHeaderBytes: 8192, ErrorLog: log.New(io.Discard, "", 0)}
	go func() { defer close(g.done); _ = g.server.Serve(listener) }()
	return g, nil
}

func (g *AgentGateway) Close() {
	g.closeOnce.Do(func() {
		g.lifecycle.Lock()
		g.closed = true
		g.lifecycle.Unlock()
		g.cancel()
		_ = g.server.Close()
		<-g.done
		g.bridge.Close()
		g.handlers.Wait()
	})
}

// Final returns only the verified final answer. Usage is accounted per journaled
// provider turn, never as an adapter-supplied aggregate in HarnessOutput.
func (g *AgentGateway) Final() (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.fault != nil || g.final == nil || g.pending != 0 {
		return "", ErrProjection
	}
	return g.final.Text, nil
}
func (g *AgentGateway) invoke(ctx context.Context, x runtime.ToolExecution) (runtime.ToolResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.fault != nil || g.ctx.Err() != nil || g.pending < 1 || g.final != nil {
		return runtime.ToolResult{}, ErrProjection
	}
	result, e := g.config.Session.Invoke(ctx, x)
	if e != nil {
		g.fault = e
		return runtime.ToolResult{}, e
	}
	if len(result.Content) > 1<<20 {
		g.fault = ErrProjection
		return runtime.ToolResult{}, ErrProjection
	}
	g.messages = append(g.messages, providers.Message{Role: "tool", ToolCallID: x.Call.ID, Content: result.Content, ToolFailed: result.Failed})
	g.pending--
	if result.EndToolUse && !result.Failed {
		g.ended = true
	}
	return result, nil
}
func (g *AgentGateway) serve(w http.ResponseWriter, r *http.Request) {
	g.lifecycle.Lock()
	if g.closed {
		g.lifecycle.Unlock()
		http.Error(w, "gateway closed", 503)
		return
	}
	g.handlers.Add(1)
	g.lifecycle.Unlock()
	defer g.handlers.Done()
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path == "/v1/tool" {
		g.bridge.ServeHTTP(w, r)
		return
	}
	deny := func(code int) { http.Error(w, "native agent request denied", code) }
	host, _, e := net.SplitHostPort(r.RemoteAddr)
	if e != nil || !net.ParseIP(host).IsLoopback() || r.Method != "POST" || r.URL.Path != "/v1/chat/completions" || r.URL.RawQuery != "" || r.URL.RawPath != "" || r.Header.Get("Origin") != "" || len(r.Header.Values("Authorization")) != 1 || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+g.Token)) != 1 {
		deny(403)
		return
	}
	body, e := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxRecordBytes))
	if e != nil || !validAgentRequest(body, g.config.Model, g.config.MaxOutputTokens, g.config.DefaultMissingOutputLimit) {
		deny(400)
		return
	}
	if !g.mu.TryLock() {
		deny(409)
		return
	}
	defer g.mu.Unlock()
	var canonical any
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	_ = d.Decode(&canonical)
	canonicalBody, _ := json.Marshal(canonical)
	digest := sha256.Sum256(canonicalBody)
	if g.ctx.Err() != nil || g.fault != nil || g.final != nil || g.pending != 0 || g.requests[digest] || len(g.requests) >= 64 {
		deny(409)
		return
	}
	upstream, e := g.requestBody()
	if e != nil {
		g.fault = e
		deny(400)
		return
	}
	g.requests[digest] = true
	run, cancel := context.WithCancel(g.ctx)
	defer cancel()
	stop := context.AfterFunc(r.Context(), cancel)
	defer stop()
	turn, attempt, e := g.config.Session.BeginTurn(run)
	if e != nil {
		g.fault = e
		deny(409)
		return
	}
	request, e := http.NewRequestWithContext(run, "POST", g.target, bytes.NewReader(upstream))
	if e != nil {
		g.fault = e
		deny(502)
		return
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	if g.config.UpstreamProtocol == "ollama" {
		request.Header.Set("Accept", "application/x-ndjson")
	}
	if g.config.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+g.config.APIKey)
	}
	response, e := g.client.Do(request)
	if e != nil {
		g.fault = e
		deny(502)
		return
	}
	defer response.Body.Close()
	native := g.config.UpstreamProtocol == "ollama"
	contentType := strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0])
	if response.StatusCode != 200 || (!native && contentType != "text/event-stream") || (native && contentType != "application/x-ndjson" && contentType != "application/json") {
		g.fault = ErrProjection
		deny(502)
		return
	}
	var completed Completion
	if native {
		completed, e = ollamaAgentCompletion(response.Body, g.config.Model)
	} else {
		completed, e = VerifyAgentCompletion(response.Body, g.config.Model)
	}
	if e != nil || run.Err() != nil {
		g.fault = ErrProjection
		deny(502)
		return
	}
	// Commit before registration and before releasing a byte to the child.
	calls, e := g.config.Session.CompleteTurn(run, turn, attempt, runtime.HarnessTurnOutput{Actual: g.config.Actual, Text: completed.Text, Calls: completed.Calls, Usage: completed.Usage})
	if e != nil {
		g.fault = e
		deny(502)
		return
	}
	for _, x := range calls {
		if !g.hasTool(x.Call.Name) {
			g.fault = ErrProjection
			deny(502)
			return
		}
	}
	for _, x := range calls {
		if e = g.bridge.Register(x); e != nil {
			g.fault = e
			deny(502)
			return
		}
	}
	g.messages = append(g.messages, providers.Message{Role: "assistant", Content: completed.Text, ToolCalls: completed.Calls})
	g.pending = len(calls)
	if g.pending == 0 {
		g.final = &completed
	}
	w.Header().Set("Content-Type", "text/event-stream")
	if _, e = w.Write(completed.Stream); e != nil {
		g.fault = e
	}
}
func (g *AgentGateway) hasTool(name string) bool {
	// Decode immutable schema bytes, not the caller's original mutable slice.
	var schemas []struct{ Function struct{ Name string } }
	if json.Unmarshal(g.tools, &schemas) != nil {
		return false
	}
	for _, s := range schemas {
		if s.Function.Name == name {
			return true
		}
	}
	return false
}
