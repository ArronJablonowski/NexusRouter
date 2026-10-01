package openclaw

import (
	"encoding/json"
	"github.com/ArronJablonowski/NexusRouter/harness/internal/textgateway"
	"os"
	"path/filepath"
	"time"
)

func agentFiles(dir, configPath string, a *agentExecution, gateway *textgateway.AgentGateway, timeout time.Duration) (string, error) {
	plugin := filepath.Join(dir, "host-plugin")
	if os.Mkdir(plugin, 0700) != nil {
		return "", ErrRun
	}
	tools, e := agentToolEntries(a.config.Tools)
	if e != nil {
		return "", e
	}
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = "nexus__" + t.Name
	}
	receipts := filepath.Join(dir, "host-receipts.jsonl")
	if os.WriteFile(receipts, nil, 0600) != nil {
		return "", ErrRun
	}
	files := map[string]any{
		"openclaw.plugin.json": map[string]any{"id": "nexus-host", "name": "NexusRouter host tools", "activation": map[string]bool{"onStartup": true}, "contracts": map[string]any{"tools": names}, "configSchema": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{}}},
		"host.json":            map[string]any{"tools": tools, "url": gateway.BaseURL[:len(gateway.BaseURL)-3] + "/v1/tool", "token": gateway.ToolToken, "timeout_ms": timeout.Milliseconds(), "receipts": receipts},
	}
	for name, value := range files {
		b, e := json.Marshal(value)
		if e != nil || os.WriteFile(filepath.Join(plugin, name), b, 0600) != nil {
			return "", ErrRun
		}
	}
	if os.WriteFile(filepath.Join(plugin, "index.js"), []byte(agentBridgeSource), 0600) != nil {
		return "", ErrRun
	}
	b, e := os.ReadFile(configPath)
	if e != nil {
		return "", ErrRun
	}
	var cfg map[string]any
	if json.Unmarshal(b, &cfg) != nil {
		return "", ErrProjection
	}
	cfg["plugins"] = map[string]any{"enabled": true, "allow": []string{"nexus-host"}, "load": map[string]any{"paths": []string{plugin}}, "entries": map[string]any{"nexus-host": map[string]bool{"enabled": true}}}
	cfg["tools"] = map[string]any{"profile": "full", "allow": names, "toolSearch": false, "codeMode": map[string]bool{"enabled": false}}
	b, e = json.Marshal(cfg)
	if e != nil || os.WriteFile(configPath, b, 0600) != nil {
		return "", ErrRun
	}
	return receipts, nil
}
