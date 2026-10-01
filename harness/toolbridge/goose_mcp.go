package toolbridge

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/ArronJablonowski/NexusRouter/harness/internal/wirejson"
	"github.com/ArronJablonowski/NexusRouter/providers"
)

// GooseMCP is a private stateless MCP projection for the pinned Goose client.
// It exposes only host-supplied schemas and already verified call IDs, never raw
// tool authority. Mount at /mcp on a private loopback server. Token is the bridge
// token; the host must join all HTTP handlers and Close the bridge on shutdown.
// The host registers calls in provider order before releasing them to Goose.
func GooseMCP(b *Bridge, tools []providers.Tool) (http.Handler, error) {
	if b == nil || !b.ordered || len(tools) == 0 || len(tools) > 128 {
		return nil, ErrDenied
	}
	names := map[string]bool{}
	list := make([]any, 0, len(tools))
	for _, t := range tools {
		if !mcpName(t.Name) || names[t.Name] || len(t.Description) > 65536 || !utf8.ValidString(t.Description) || !wirejson.Unique(t.Parameters) {
			return nil, ErrDenied
		}
		var schema map[string]json.RawMessage
		if json.Unmarshal(t.Parameters, &schema) != nil || schema == nil {
			return nil, ErrDenied
		}
		var kind string
		if json.Unmarshal(schema["type"], &kind) != nil || kind != "object" {
			return nil, ErrDenied
		}
		names[t.Name] = true
		list = append(list, map[string]any{"name": t.Name, "description": t.Description, "inputSchema": json.RawMessage(t.Parameters)})
	}
	body, err := json.Marshal(map[string]any{"tools": list})
	if err != nil || len(body) > 512<<10 {
		return nil, ErrDenied
	}
	return &gooseMCP{bridge: b, names: names, list: body}, nil
}

type gooseMCP struct {
	bridge *Bridge
	names  map[string]bool
	list   json.RawMessage
}

func mcpName(name string) bool {
	return len(name) > 0 && len(name) <= 64 && !strings.ContainsFunc(name, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-')
	})
}
func (m *gooseMCP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	deny := func(code int) { http.Error(w, "native MCP request denied", code) }
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || !net.ParseIP(host).IsLoopback() || r.URL.Path != "/mcp" || r.URL.RawPath != "" || r.URL.RawQuery != "" || r.Header.Get("Origin") != "" || len(r.Header.Values("Authorization")) != 1 || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+m.bridge.Token())) != 1 {
		deny(403)
		return
	}
	if r.Method != "POST" {
		deny(405)
		return
	}
	if strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]) != "application/json" {
		deny(400)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 128<<10))
	if err != nil || !wirejson.Unique(body) {
		deny(400)
		return
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil || fields == nil {
		deny(400)
		return
	}
	for k := range fields {
		if k != "jsonrpc" && k != "id" && k != "method" && k != "params" {
			deny(400)
			return
		}
	}
	var version, method string
	if json.Unmarshal(fields["jsonrpc"], &version) != nil || version != "2.0" || json.Unmarshal(fields["method"], &method) != nil {
		deny(400)
		return
	}
	id, hasID := fields["id"]
	if hasID && !mcpID(id) {
		deny(400)
		return
	}
	var params map[string]json.RawMessage
	if raw, ok := fields["params"]; ok && (json.Unmarshal(raw, &params) != nil || params == nil) {
		deny(400)
		return
	}
	reply := func(result any, code int) {
		w.Header().Set("Content-Type", "application/json")
		payload := map[string]any{"jsonrpc": "2.0", "id": id}
		if code != 0 {
			payload["error"] = map[string]any{"code": code, "message": "native MCP request unavailable"}
		} else {
			payload["result"] = result
		}
		_ = json.NewEncoder(w).Encode(payload)
	}
	if !hasID {
		if method != "notifications/initialized" {
			deny(400)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		return
	}
	switch method {
	case "initialize":
		var protocol string
		if json.Unmarshal(params["protocolVersion"], &protocol) != nil || protocol != "2025-11-25" {
			reply(nil, -32602)
			return
		}
		reply(map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "nexus-host-tools", "version": "1"}}, 0)
	case "tools/list":
		for k := range params {
			if k != "_meta" {
				reply(nil, -32602)
				return
			}
		}
		reply(m.list, 0)
	case "tools/call":
		// Goose forwards the original provider call ID in this metadata field.
		// Session, cwd and progress metadata never select authority or arguments.
		for k := range params {
			if k != "name" && k != "arguments" && k != "_meta" {
				reply(nil, -32602)
				return
			}
		}
		var name, callID string
		var meta map[string]json.RawMessage
		if json.Unmarshal(params["name"], &name) != nil || !m.names[name] || json.Unmarshal(params["_meta"], &meta) != nil || json.Unmarshal(meta["agent-tool-call-request-id"], &callID) != nil || !label(callID, 256) {
			reply(nil, -32602)
			return
		}
		m.bridge.mu.Lock()
		entry := m.bridge.calls[callID]
		matched := entry != nil && entry.execution.Call.Name == name && mcpArgumentsEqual(entry.execution.Call.Arguments, params["arguments"])
		m.bridge.mu.Unlock()
		if !matched {
			reply(nil, -32602)
			return
		}
		result, err := m.bridge.execute(r.Context(), callID)
		if err != nil {
			reply(nil, -32000)
			return
		}
		reply(map[string]any{"content": []any{map[string]string{"type": "text", "text": result.Content}}, "isError": result.Failed}, 0)
	default:
		// Includes Goose's optional discovery probe. Unsupported operations cannot
		// expose prompts/resources, invoke sampling, mutate tools or run commands.
		reply(nil, -32601)
	}
}
func mcpID(raw json.RawMessage) bool {
	if len(raw) == 0 || len(raw) > 256 {
		return false
	}
	var value any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if d.Decode(&value) != nil {
		return false
	}
	switch v := value.(type) {
	case string:
		return label(v, 128)
	case json.Number:
		_, e := v.Int64()
		return e == nil
	}
	return false
}
func mcpArgumentsEqual(a, b json.RawMessage) bool {
	decode := func(raw json.RawMessage) (map[string]any, error) {
		if len(raw) > 64<<10 || !wirejson.Unique(raw) {
			return nil, ErrDenied
		}
		var v map[string]any
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		e := d.Decode(&v)
		if e != nil || v == nil {
			return nil, ErrDenied
		}
		return v, nil
	}
	x, e := decode(a)
	if e != nil {
		return false
	}
	y, e := decode(b)
	return e == nil && reflect.DeepEqual(x, y)
}
