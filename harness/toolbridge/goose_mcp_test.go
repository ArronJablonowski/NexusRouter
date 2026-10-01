package toolbridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func mcpFixture(t *testing.T) (*Bridge, http.Handler, *int) {
	t.Helper()
	effects := new(int)
	b, e := NewOrdered(context.Background(), 8, time.Second, func(_ context.Context, x runtime.ToolExecution) (runtime.ToolResult, error) {
		*effects++
		return runtime.ToolResult{Content: x.Call.ID, Effect: runtime.NoEffect}, nil
	})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(b.Close)
	x := proposal()
	x.Call.Name = "lookup"
	x.Call.ID = "call-1"
	x.Call.Arguments = json.RawMessage(`{"path":"safe","n":9007199254740993}`)
	if e = b.Register(x); e != nil {
		t.Fatal(e)
	}
	h, e := GooseMCP(b, []providers.Tool{{Name: "lookup", Description: "Host lookup", Parameters: json.RawMessage(`{"type":"object"}`)}})
	if e != nil {
		t.Fatal(e)
	}
	return b, h, effects
}
func mcpRequest(b *Bridge, body string) *http.Request {
	r := httptest.NewRequest("POST", "http://127.0.0.1/mcp", strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:1111"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+b.Token())
	return r
}

const mcpCall = `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"lookup","arguments":{"n":9007199254740993,"path":"safe"},"_meta":{"agent-tool-call-request-id":"call-1","agent-working-dir":"/untrusted","agent-session-id":"untrusted"}}}`

func TestGooseMCPBindingAndCachedRetry(t *testing.T) {
	b, h, effects := mcpFixture(t)
	for range 2 {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, mcpRequest(b, mcpCall))
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"text":"call-1"`) || strings.Contains(w.Body.String(), `"error"`) {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if *effects != 1 {
		t.Fatal("duplicate effect", *effects)
	}
}
func TestGooseMCPRejectsChangedAuthority(t *testing.T) {
	for _, body := range []string{
		strings.Replace(mcpCall, "call-1", "unknown", 1),
		strings.Replace(mcpCall, `"name":"lookup"`, `"name":"shell"`, 1),
		strings.Replace(mcpCall, `"path":"safe"`, `"path":"elsewhere"`, 1),
		strings.Replace(mcpCall, "9007199254740993", "9007199254740992", 1),
		strings.Replace(mcpCall, `"path":"safe"`, `"path":"safe","path":"safe"`, 1),
		strings.Replace(mcpCall, `"agent-tool-call-request-id":"call-1"`, `"other":"call-1"`, 1),
		strings.Replace(mcpCall, `"arguments":{"n":9007199254740993,"path":"safe"}`, `"arguments":null`, 1),
		strings.Replace(mcpCall, `"id":3`, `"id":null`, 1),
		`[` + mcpCall + `]`,
	} {
		b, h, effects := mcpFixture(t)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, mcpRequest(b, body))
		if w.Code == 200 && !strings.Contains(w.Body.String(), `"error"`) {
			t.Fatal("accepted", body, w.Body.String())
		}
		if *effects != 0 {
			t.Fatal("unverified effect")
		}
	}
}
func TestGooseMCPTransportBoundary(t *testing.T) {
	for _, mode := range []string{"remote", "token", "duplicate_auth", "origin", "query", "rawpath", "method", "type"} {
		t.Run(mode, func(t *testing.T) {
			b, h, effects := mcpFixture(t)
			r := mcpRequest(b, mcpCall)
			switch mode {
			case "remote":
				r.RemoteAddr = "192.0.2.1:33"
			case "token":
				r.Header.Set("Authorization", "Bearer wrong")
			case "duplicate_auth":
				r.Header.Add("Authorization", r.Header.Get("Authorization"))
			case "origin":
				r.Header.Set("Origin", "https://other")
			case "query":
				r.URL.RawQuery = "x=1"
			case "rawpath":
				r.URL.RawPath = "/%6dcp"
			case "method":
				r.Method = "GET"
			case "type":
				r.Header.Set("Content-Type", "text/plain")
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code < 400 || *effects != 0 {
				t.Fatal(mode, w.Code, *effects)
			}
		})
	}
}
func TestGooseMCPNegotiationAndCatalogue(t *testing.T) {
	b, h, effects := mcpFixture(t)
	for _, tc := range []struct {
		body, want string
		status     int
	}{
		{`{"jsonrpc":"2.0","id":0,"method":"server/discover"}`, `"code":-32601`, 200},
		{`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`, `"protocolVersion":"2025-11-25"`, 200},
		{`{"jsonrpc":"2.0","method":"notifications/initialized"}`, "", 202},
		{`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{"_meta":{"progressToken":0}}}`, `"name":"lookup"`, 200},
		{`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{"cursor":"else"}}`, `"code":-32602`, 200},
		{`{"jsonrpc":"2.0","id":2,"method":"resources/read"}`, `"code":-32601`, 200},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, mcpRequest(b, tc.body))
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.want) {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if *effects != 0 {
		t.Fatal("discovery invoked tool")
	}
}

func TestGooseMCPCatalogueIsOwnedAndValidated(t *testing.T) {
	b, _, _ := mcpFixture(t)
	schema := json.RawMessage(`{"type":"object","properties":{}}`)
	tools := []providers.Tool{{Name: "lookup", Parameters: schema}}
	h, e := GooseMCP(b, tools)
	if e != nil {
		t.Fatal(e)
	}
	tools[0].Name = "mutated"
	for i := range schema {
		schema[i] = 'x'
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, mcpRequest(b, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if !strings.Contains(w.Body.String(), `"name":"lookup"`) || strings.Contains(w.Body.String(), "mutated") {
		t.Fatal(w.Body.String())
	}
	for _, bad := range [][]providers.Tool{
		nil,
		{{Name: "lookup", Parameters: json.RawMessage(`null`)}},
		{{Name: "lookup", Parameters: json.RawMessage(`{"type":"array"}`)}},
		{{Name: "lookup", Parameters: json.RawMessage(`{"type":"object","type":"object"}`)}},
		{{Name: "bad name", Parameters: json.RawMessage(`{"type":"object"}`)}},
		{{Name: "lookup", Parameters: json.RawMessage(`{"type":"object"}`)}, {Name: "lookup", Parameters: json.RawMessage(`{"type":"object"}`)}},
	} {
		if _, e := GooseMCP(b, bad); e == nil {
			t.Fatal("invalid catalogue accepted", bad)
		}
	}
	unordered, e := New(context.Background(), 1, time.Second, func(context.Context, runtime.ToolExecution) (runtime.ToolResult, error) {
		t.Fatal("unexpected invocation")
		return runtime.ToolResult{}, nil
	})
	if e != nil {
		t.Fatal(e)
	}
	defer unordered.Close()
	if _, e := GooseMCP(unordered, []providers.Tool{{Name: "lookup", Parameters: json.RawMessage(`{"type":"object"}`)}}); e == nil {
		t.Fatal("unordered bridge allowed")
	}
}
