package hermes

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
)

// isolatedFiles prepares trusted native Hermes configuration, not an OS sandbox.
// The only credential supplied here is a disposable loopback gateway token.
// A runner must still verify every response and enforce process admission.
func isolatedFiles(dir, gatewayURL, gatewayKey, model string) (string, []string, error) {
	u, err := url.Parse(gatewayURL)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Path != "/v1" || u.RawPath != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil || !label(gatewayKey) || !label(model) || !filepath.IsAbs(dir) {
		return "", nil, ErrProjection
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return "", nil, ErrProjection
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return "", nil, ErrProjection
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		return "", nil, ErrProjection
	}
	config := map[string]any{
		"model":               map[string]any{"default": model, "provider": "nexus-gateway", "base_url": gatewayURL},
		"providers":           map[string]any{"nexus-gateway": map[string]any{"base_url": gatewayURL, "api_key": gatewayKey, "default_model": model, "transport": "chat_completions"}},
		"agent":               map[string]any{"max_turns": 1, "disabled_toolsets": []string{"all"}},
		"auxiliary":           map[string]any{"title_generation": map[string]bool{"model_upgrade_enabled": false}},
		"fallback_model":      []string{},
		"memory":              map[string]bool{"memory_enabled": false, "user_profile_enabled": false},
		"compression":         map[string]bool{"enabled": false},
		"checkpoints":         map[string]bool{"enabled": false},
		"smart_model_routing": map[string]bool{"enabled": false},
		"mcp_servers":         map[string]any{},
	}
	body, err := json.Marshal(config)
	if err != nil {
		return "", nil, err
	}
	path := filepath.Join(dir, "config.yaml") // JSON is a YAML subset accepted by Hermes.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", nil, err
	}
	_, writeErr := f.Write(body)
	closeErr := f.Close()
	if writeErr != nil {
		return "", nil, writeErr
	}
	if closeErr != nil {
		return "", nil, closeErr
	}
	// HERMES_SAFE_MODE disables plugins/MCP. Do not pass --safe-mode: that CLI
	// flag also ignores this explicit configuration. --ignore-rules is separate.
	env := []string{"PATH=" + os.Getenv("PATH"), "HERMES_HOME=" + dir, "HERMES_CONFIG_PATH=" + path, "HERMES_SAFE_MODE=1", "HERMES_IGNORE_RULES=1", "NO_COLOR=1", "HERMES_DISABLE_LAZY_INSTALLS=1"}
	return path, env, nil
}
