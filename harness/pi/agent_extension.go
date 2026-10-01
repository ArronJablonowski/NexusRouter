package pi

import (
	_ "embed"
	"encoding/json"
	"net"
	"net/url"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/ArronJablonowski/NexusRouter/harness/internal/wirejson"
	"github.com/ArronJablonowski/NexusRouter/providers"
)

//go:embed agent_extension.mjs
var agentExtension string

// Render only trusted host schemas into a private per-run module. The native
// process receives a bridge token, never provider credentials or tool authority.
// The caller must disable extension discovery and all built-in tools and load
// only this generated module. Pi and Node are trusted installed dependencies.
func renderAgentExtension(base, token string, timeout time.Duration, tools []providers.Tool) ([]byte, error) {
	u, e := url.Parse(base)
	if e != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "/v1" || u.RawPath != "" || u.Port() == "" || !net.ParseIP(u.Hostname()).IsLoopback() || !regexp.MustCompile(`^[a-zA-Z0-9_-]{20,128}$`).MatchString(token) || timeout < time.Millisecond || timeout > 15*time.Minute || len(tools) < 1 || len(tools) > 128 {
		return nil, ErrProtocol
	}
	type tool struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	}
	entries := make([]tool, 0, len(tools))
	seen := map[string]bool{}
	for _, t := range tools {
		if !regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`).MatchString(t.Name) || seen[t.Name] || !utf8.ValidString(t.Description) || len(t.Description) > 65536 || !wirejson.Unique(t.Parameters) {
			return nil, ErrProtocol
		}
		var schema map[string]json.RawMessage
		if json.Unmarshal(t.Parameters, &schema) != nil || schema == nil {
			return nil, ErrProtocol
		}
		var kind string
		if json.Unmarshal(schema["type"], &kind) != nil || kind != "object" {
			return nil, ErrProtocol
		}
		seen[t.Name] = true
		entries = append(entries, tool{t.Name, t.Description, t.Parameters})
	}
	config, e := json.Marshal(struct {
		Base    string `json:"base_url"`
		Token   string `json:"token"`
		Timeout int64  `json:"timeout_ms"`
		Tools   []tool `json:"tools"`
	}{base, token, timeout.Milliseconds(), entries})
	if e != nil || len(config) > MaxRecordBytes/2 {
		return nil, ErrProtocol
	}
	return append(append([]byte("const nexusBridge = "), config...), []byte(";\n"+agentExtension)...), nil
}
